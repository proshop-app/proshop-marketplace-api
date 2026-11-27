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
	clock  *fake.Scheduler
}

// Бюджет, переданный вызывающим данными, ограждение ставит само и засчитывает его истечение магазину.
func TestCallerBudgetAsDataCountsHangToTenant(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	metrics := &fake.PlatformMetrics{}
	clock := guardTestClock()
	c := New(&hungTenant{PlatformAPI: fake.NewPlatform(), tenant: "t1", clock: clock}, cfg, clock, metrics, nil)
	ctx := ports.WithPlatformCallBudget(context.Background(), cfg.StrictTimeout/4)
	started := clock.Now()
	if _, err := c.MarketplaceSettings(ctx, "t1"); !errors.Is(err, ErrTimeout) {
		t.Fatal(err)
	}
	if took := clock.Now().Sub(started); took != cfg.StrictTimeout/4 {
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
	clock := guardTestClock()
	upstream.CallHook = func(ctx context.Context, _ string) error {
		return clock.Expire(ctx)
	}
	metrics := &fake.PlatformMetrics{}
	c := New(upstream, cfg, clock, metrics, nil)
	for n := range cfg.FailureMinCalls {
		_, _ = c.MarketplaceSettings(context.Background(), ids.TenantID(fmt.Sprintf("t%d", n)))
	}
	_, err := c.MarketplaceSettings(context.Background(), "fresh")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal("массовое зависание не открыло общий размыкатель", err)
	}
	if metrics.Count("settings", "server_breaker") != 1 {
		t.Fatal("отказ дан не общим размыкателем")
	}
	var rejected *ports.PlatformRejected
	if !errors.As(err, &rejected) || !rejected.Server {
		t.Fatal("отказ общего размыкателя не помечен как отказ сервера", err)
	}
}

// Один зависший магазин не размыкает сервер для остальных: его зависания до открытия своего
// размыкателя идут в долю окна, а пробы открытого размыкателя — нет.
func TestOneHungShopDoesNotOpenServerBreaker(t *testing.T) {
	cfg := testConfig()
	clock := guardTestClock()
	c := New(&hungTenant{PlatformAPI: fake.NewPlatform(), tenant: "t1", clock: clock}, cfg, clock, nil, nil)
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

func TestRepeatedStrictTimeoutsCountOnceBeforeShopBreakerOpens(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 10
	upstream := fake.NewPlatform()
	upstream.Fail("t1", context.DeadlineExceeded)
	clock := guardTestClock()
	c := New(upstream, cfg, clock, nil, nil)
	for range 4 {
		if _, err := c.MarketplaceSettings(context.Background(), "t1"); !errors.Is(err, ErrTimeout) {
			t.Fatal(err)
		}
	}
	if c.server.window.failures != 1 || !c.tenants["t1"].breaker.openUntil.IsZero() {
		t.Fatal("повторные таймауты одного магазина попали в окно сервера", c.server.window.failures)
	}
	if _, excluded := c.CreationCandidates(true); len(excluded) != 1 || excluded[0] != "t1" {
		t.Fatal("недавнее молчание должно исключить магазин из пробы", excluded)
	}
	clock.Advance(cfg.FailureWindow)
	if _, excluded := c.CreationCandidates(true); len(excluded) != 0 {
		t.Fatal("давний таймаут исключил магазин из пробы", excluded)
	}
}

func TestResponseFromInFlightShopClosesServerBreaker(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	clock := guardTestClock()
	c := New(fake.NewPlatform(), cfg, clock, nil, nil)
	healthy := admit(t, c, "t1", checkoutClass)
	failed := admit(t, c, "t2", checkoutClass)
	c.release(failed, &ports.PlatformServerFailure{Cause: ErrUnavailable})
	if c.ServerState() != ports.PlatformServerBlocked {
		t.Fatal("общий отказ должен открыть размыкатель")
	}
	c.release(healthy, nil)
	if c.ServerState() != ports.PlatformServerReady {
		t.Fatal("ответ магазина не закрыл размыкатель сервера")
	}
}

// Короткий бюджет чтения не доказывает зависание сервера: медленная сверка или отзывы многих
// магазинов не закрывают оформление всем.
func TestReadTimeoutsDoNotOpenServerBreaker(t *testing.T) {
	cfg := testConfig()
	upstream := fake.NewPlatform()
	clock := guardTestClock()
	upstream.CallHook = func(ctx context.Context, operation string) error {
		if operation == "order_statuses" {
			return clock.Expire(ctx)
		}
		return ctx.Err()
	}
	c := New(upstream, cfg, clock, nil, nil)
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
	clock := guardTestClock()
	upstream := fake.NewPlatform()
	upstream.CallHook = func(ctx context.Context, _ string) error {
		deadline, _ := ctx.Deadline()
		if granted := deadline.Sub(clock.Now()); granted != cfg.StrictTimeout {
			t.Error("срок магазина должен начинаться после получения места", granted)
		}
		clock.Advance(cfg.StrictTimeout - time.Millisecond)
		return ctx.Err()
	}
	c := New(upstream, cfg, clock, nil, nil)
	first := admit(t, c, "t1", readClass)
	second := admit(t, c, "t3", readClass)
	done := make(chan error, 1)
	go func() {
		_, err := c.MarketplaceSettings(context.Background(), "t2")
		done <- err
	}()
	awaitQueued(c, readClass)
	clock.Advance(cfg.StrictTimeout - time.Millisecond)
	c.release(first, nil)
	if err := <-done; err != nil {
		t.Fatal("магазин должен получить полный срок после ожидания места", err)
	}
	c.release(second, nil)
	if c.tenants["t2"].breaker.failures != 0 || c.server.window.failures != 0 {
		t.Fatal("ожидание места засчитано магазину или серверу")
	}
}

// Отказ размыкателя несёт момент допуска; после интервала вызов делает одна проба,
// остальные снова получают «не раньше», а не идут все разом.
func TestRejectionCarriesReopenTimeAndProbeIsSingle(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	clock := guardTestClock()
	c := New(&hungTenant{PlatformAPI: fake.NewPlatform(), tenant: "t1", clock: clock}, cfg, clock, nil, nil)
	_, _ = c.MarketplaceSettings(context.Background(), "t1")
	_, err := c.MarketplaceSettings(context.Background(), "t1")
	var rejected *ports.PlatformRejected
	if !errors.As(err, &rejected) || !errors.Is(err, ErrUnavailable) ||
		!rejected.NotBefore.Equal(clock.Now().Add(cfg.OpenInterval)) {
		t.Fatal("отказ размыкателя без момента допуска", err)
	}
	clock.Advance(cfg.OpenInterval)
	started := make(chan struct{})
	upstream := fake.NewPlatform()
	upstream.CallHook = func(ctx context.Context, _ string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	c.upstream = upstream
	probe := make(chan error, 1)
	go func() {
		_, err := c.MarketplaceSettings(context.Background(), "t1")
		probe <- err
	}()
	<-started
	_, err = c.MarketplaceSettings(context.Background(), "t1")
	if !errors.As(err, &rejected) || !rejected.NotBefore.After(clock.Now()) {
		t.Fatal("во время пробы второй вызов не получил «не раньше»", err)
	}
	clock.Advance(cfg.StrictTimeout)
	if err := <-probe; !errors.Is(err, ErrTimeout) {
		t.Fatal("проба не дошла до магазина", err)
	}
}

// Срок вызова, а с ним заголовок x-marketplace-deadline-ms, не дальше крайнего момента вызывающего.
func TestCallDeadlineLimitsGrantedTime(t *testing.T) {
	cfg := testConfig()
	clock := guardTestClock()
	upstream := fake.NewPlatform()
	want := clock.Now().Add(cfg.StrictTimeout / 4)
	upstream.CallHook = func(ctx context.Context, _ string) error {
		deadline, _ := ctx.Deadline()
		if !deadline.Equal(want) {
			t.Error("срок вызова отличается от крайнего момента вызывающего", deadline, want)
		}
		return nil
	}
	c := New(upstream, cfg, clock, nil, nil)
	ctx := ports.WithPlatformCallDeadline(context.Background(), want)
	if _, err := c.MarketplaceSettings(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
}

func (p *hungTenant) MarketplaceSettings(ctx context.Context, tenant ids.TenantID) (ports.ShopSettings, error) {
	if tenant == p.tenant {
		return ports.ShopSettings{}, p.clock.Expire(ctx)
	}
	return p.PlatformAPI.MarketplaceSettings(ctx, tenant)
}

func TestCreationWakesWhenServerProbeFinishes(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	cfg.StrictTimeout = 5 * time.Second
	clock := guardTestClock()
	guard := New(fake.NewPlatform(), cfg, clock, nil, nil)
	notifications := 0
	guard.SetServerChanged(func() { notifications++ })

	failed := admit(t, guard, "t1", checkoutClass)
	guard.release(failed, &ports.PlatformServerFailure{Cause: ErrUnavailable})
	if got := guard.ServerWorkAt(); !got.Equal(clock.Now().Add(cfg.OpenInterval)) || notifications != 1 {
		t.Fatalf("открытие размыкателя: срок %v, сигналов %d", got, notifications)
	}

	clock.Advance(cfg.OpenInterval)
	probe := admit(t, guard, "t2", readClass)
	if !probe.server.probe {
		t.Fatal("ожидалась проба сервера")
	}
	want := clock.Now().Add(200 * time.Millisecond)
	if got := guard.ServerWorkAt(); !got.Equal(want) {
		t.Fatalf("срок проверки пробы %v, нужен %v", got, want)
	}
	clock.Advance(10 * time.Millisecond)
	guard.release(probe, nil)
	if got := guard.ServerWorkAt(); !got.IsZero() || notifications != 2 {
		t.Fatalf("завершение пробы: срок %v, сигналов %d", got, notifications)
	}
}
