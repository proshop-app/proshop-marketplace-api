package platformguard

import (
	"context"
	"errors"
	"fmt"
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

func (p *hungTenant) MarketplaceSettings(ctx context.Context, tenant ids.TenantID) (ports.ShopSettings, error) {
	if tenant == p.tenant {
		<-ctx.Done()
		return ports.ShopSettings{}, ctx.Err()
	}
	return p.PlatformAPI.MarketplaceSettings(ctx, tenant)
}
