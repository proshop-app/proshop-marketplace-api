package platformguard

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// hungTenant не отвечает на настройки одного магазина до истечения срока вызова.
type hungTenant struct {
	ports.PlatformAPI
	tenant ids.TenantID
}

// slowShops отвечает каждому магазину через заданное время, если срок вызова позволяет.
type slowShops struct {
	ports.PlatformAPI
	delays  map[ids.TenantID]time.Duration
	entered chan ids.TenantID
	mu      sync.Mutex
	granted map[ids.TenantID][]time.Duration
}

// Бюджет, переданный вызывающим данными, ограждение ставит само и засчитывает его истечение магазину.
func TestCallerBudgetAsDataCountsHangToTenant(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	metrics := &fake.PlatformMetrics{}
	c := New(&hungTenant{PlatformAPI: fake.NewPlatform(), tenant: "t1"}, cfg, fake.NewClock(time.Now()), metrics, nil)
	ctx := ports.WithPlatformCallBudget(context.Background(), cfg.StrictTimeout/4)
	started := time.Now()
	if _, err := c.MarketplaceSettings(ctx, "t1"); !errors.Is(err, ErrTimeout) {
		t.Fatal(err)
	}
	if took := time.Since(started); took >= cfg.StrictTimeout {
		t.Fatal("бюджет вызывающего не сократил срок ограждения", took)
	}
	if metrics.Count("settings", "timeout") != 1 {
		t.Fatal("зависание учтено не как timeout")
	}
	if _, err := c.MarketplaceSettings(context.Background(), "t1"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("зависание по бюджету вызывающего не открыло размыкатель магазина", err)
	}
}

// Зависание многих магазинов — зависание общего сервера: размыкается общий размыкатель,
// и следующий магазин отклоняется сразу, не ожидая своих пяти сроков.
func TestWidespreadHangOpensServerBreaker(t *testing.T) {
	cfg := testConfig()
	upstream := fake.NewPlatform()
	upstream.CallHook = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	metrics := &fake.PlatformMetrics{}
	c := New(upstream, cfg, fake.NewClock(time.Now()), metrics, nil)
	for n := range cfg.FailureMinCalls {
		_, _ = c.MarketplaceSettings(context.Background(), ids.TenantID(fmt.Sprintf("t%d", n)))
	}
	started := time.Now()
	_, err := c.MarketplaceSettings(context.Background(), "fresh")
	if !errors.Is(err, ErrUnavailable) || time.Since(started) >= cfg.StrictTimeout/2 {
		t.Fatal("массовое зависание не открыло общий размыкатель", err, time.Since(started))
	}
	if metrics.Count("settings", "server_breaker") != 1 {
		t.Fatal("отказ дан не общим размыкателем")
	}
}

// Один зависший магазин не размыкает сервер для остальных: его зависания до открытия своего
// размыкателя идут в долю окна, а пробы открытого размыкателя — нет.
func TestOneHungShopDoesNotOpenServerBreaker(t *testing.T) {
	cfg := testConfig()
	clock := fake.NewClock(time.Now())
	c := New(&hungTenant{PlatformAPI: fake.NewPlatform(), tenant: "t1"}, cfg, clock, nil, nil)
	for range cfg.FailureMinCalls + cfg.FailureThreshold {
		_, _ = c.MarketplaceSettings(context.Background(), "t1")
		clock.Advance(cfg.OpenInterval)
	}
	if !c.server.available(clock.Now()) || c.server.failures != 0 {
		t.Fatal("зависший магазин засчитан общему серверу подряд или открыл его")
	}
	if _, err := c.MarketplaceSettings(context.Background(), "t2"); err != nil {
		t.Fatal("чужое зависание закрыло магазин", err)
	}
}

// Короткий бюджет чтения не доказывает зависание сервера: медленная сверка или отзывы многих
// магазинов не закрывают оформление всем.
func TestReadTimeoutsDoNotOpenServerBreaker(t *testing.T) {
	cfg := testConfig()
	upstream := fake.NewPlatform()
	upstream.CallHook = func(ctx context.Context, operation string) error {
		if operation == "order_statuses" {
			<-ctx.Done()
		}
		return ctx.Err()
	}
	c := New(upstream, cfg, fake.NewClock(time.Now()), nil, nil)
	for n := range cfg.FailureMinCalls {
		_, _ = c.OrderStatuses(context.Background(), ids.TenantID(fmt.Sprintf("t%d", n)), []string{"o1"})
	}
	if _, err := c.MarketplaceSettings(context.Background(), "fresh"); err != nil {
		t.Fatal("таймауты чтений открыли общий размыкатель", err)
	}
}

// Ожидание места, занятого медленными вызовами магазина A, не засчитывается магазину B:
// срок B отсчитывается от начала его вызова, и B успевает ответить в свой бюджет.
func TestWaitingForSlotIsNotChargedToShop(t *testing.T) {
	cfg := testConfig()
	cfg.StrictTimeout = 200 * time.Millisecond
	cfg.TenantMaxConcurrent = 4
	upstream := &slowShops{
		PlatformAPI: fake.NewPlatform(),
		delays:      map[ids.TenantID]time.Duration{"A": 150 * time.Millisecond, "B": 150 * time.Millisecond},
		entered:     make(chan ids.TenantID, 8),
		granted:     map[ids.TenantID][]time.Duration{},
	}
	c := New(upstream, cfg, fake.NewClock(time.Now()), nil, nil)
	for range cfg.FailureThreshold + 1 {
		var wg sync.WaitGroup
		for range cfg.ReadMaxConcurrent {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = c.MarketplaceSettings(context.Background(), "A")
			}()
			<-upstream.entered
		}
		time.Sleep(60 * time.Millisecond)
		if _, err := c.MarketplaceSettings(context.Background(), "B"); err != nil {
			t.Fatal("здоровый магазин не ответил в свой бюджет после ожидания места", err)
		}
		<-upstream.entered
		wg.Wait()
	}
	if c.tenants["B"].breaker.failures != 0 || c.server.window.failures != 0 {
		t.Fatal("ожидание места засчитано магазину или серверу")
	}
	for _, granted := range upstream.granted["B"] {
		if granted < cfg.StrictTimeout*9/10 {
			t.Fatal("срок в заголовке не отражает бюджет от начала вызова", granted)
		}
	}
}

// Отказ размыкателя несёт момент допуска; после интервала вызов делает одна проба,
// остальные снова получают «не раньше», а не идут все разом.
func TestRejectionCarriesReopenTimeAndProbeIsSingle(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	clock := fake.NewClock(time.Now())
	c := New(&hungTenant{PlatformAPI: fake.NewPlatform(), tenant: "t1"}, cfg, clock, nil, nil)
	_, _ = c.MarketplaceSettings(context.Background(), "t1")
	_, err := c.MarketplaceSettings(context.Background(), "t1")
	var rejected *ports.PlatformRejected
	if !errors.As(err, &rejected) || !errors.Is(err, ErrUnavailable) ||
		!rejected.NotBefore.Equal(clock.Now().Add(cfg.OpenInterval)) {
		t.Fatal("отказ размыкателя без момента допуска", err)
	}
	clock.Advance(cfg.OpenInterval)
	probe := make(chan error, 1)
	go func() {
		_, err := c.MarketplaceSettings(context.Background(), "t1")
		probe <- err
	}()
	for !c.probing("t1") {
		time.Sleep(time.Millisecond)
	}
	_, err = c.MarketplaceSettings(context.Background(), "t1")
	if !errors.As(err, &rejected) || !rejected.NotBefore.After(clock.Now()) {
		t.Fatal("во время пробы второй вызов не получил «не раньше»", err)
	}
	if err := <-probe; !errors.Is(err, ErrTimeout) {
		t.Fatal("проба не дошла до магазина", err)
	}
}

func (c *Client) probing(tenant ids.TenantID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.tenants[tenant]
	return state != nil && state.breaker.probe
}

func (p *slowShops) MarketplaceSettings(ctx context.Context, tenant ids.TenantID) (ports.ShopSettings, error) {
	deadline, _ := ctx.Deadline()
	p.mu.Lock()
	p.granted[tenant] = append(p.granted[tenant], time.Until(deadline))
	p.mu.Unlock()
	p.entered <- tenant
	select {
	case <-time.After(p.delays[tenant]):
		return p.PlatformAPI.MarketplaceSettings(ctx, tenant)
	case <-ctx.Done():
		return ports.ShopSettings{}, ctx.Err()
	}
}

func (p *hungTenant) MarketplaceSettings(ctx context.Context, tenant ids.TenantID) (ports.ShopSettings, error) {
	if tenant == p.tenant {
		<-ctx.Done()
		return ports.ShopSettings{}, ctx.Err()
	}
	return p.PlatformAPI.MarketplaceSettings(ctx, tenant)
}
