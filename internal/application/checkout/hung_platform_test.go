package checkout_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/platformguard"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// hungCallBudget равен сроку ограждения, как в процессе: PLATFORM_API_TIMEOUT задаёт оба.
const hungCallBudget = 50 * time.Millisecond

// hangKey помечает вызов, который фейк держит до истечения срока.
type hangKey struct{}

// hungShopOrders не отвечает на создание заказа одного магазина до истечения срока вызова.
type hungShopOrders struct {
	ports.PlatformAPI
	tenant ids.TenantID
}

// leaseQueue запоминает аренду, которую исполнитель просит у очереди.
type leaseQueue struct {
	ports.TaskQueue
	mu     sync.Mutex
	leases []time.Duration
}

// deadlineUOW запоминает срок контекста каждой транзакции обработчика.
type deadlineUOW struct {
	ports.UnitOfWork
	mu        sync.Mutex
	remaining []time.Duration
	bounded   []bool
}

// Зависший магазин обязан размыкать свой размыкатель на каждом вызове, где вызывающий
// задаёт бюджет: раньше равный срок вызывающего истекал первым и засчитывался отменой.
func TestHungShopOpensTenantBreakerOnEveryBudgetedCall(t *testing.T) {
	cases := []struct {
		operation string
		run       func(t *testing.T, e *env, svc *appcheckout.Service) ids.TenantID
	}{
		{"create_order", hungCreate},
		{"get_order", hungPoll},
		{"cancel_order", hungCancel},
		{"stub_payment", hungStubPayment},
		{"quote", hungQuote},
	}
	for _, tc := range cases {
		t.Run(tc.operation, func(t *testing.T) {
			e := setup(t)
			guard, metrics := hungGuard(e)
			svc := guardedService(e, guard)
			tenant := tc.run(t, e, svc)
			if metrics.Count(tc.operation, "timeout") == 0 {
				t.Fatal("зависание учтено не как timeout", metrics.Count(tc.operation, "canceled"))
			}
			requireRejectedAtOnce(t, guard, tenant)
		})
	}
}

// При открытом размыкателе задача создания не держит место полосы весь срок вызова,
// а заказ магазина, чей вызов не завис, создаётся.
func TestHungShopReleasesCreationSlotsAndOtherShopCreates(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	guard, _ := hungGuard(e)
	hang(e)
	upstream := &hungShopOrders{PlatformAPI: guard, tenant: "t1"}
	svc := appcheckout.NewService(e.store, upstream, e.pickups, e.verifier, &fake.IDs{}, e.clock, guardedConfig())
	executor := appcheckout.NewExecutor(e.store, e.clock, svc, messaging.DefaultMaxAttempts)
	first := dispatchCreation(ctx, t, executor, 2)
	if first["t1"] < hungCallBudget {
		t.Fatal("первый вызов зависшего магазина должен ждать срок ограждения", first)
	}
	if first["t2"] >= hungCallBudget {
		t.Fatal("магазин без зависания ждал чужой срок", first)
	}
	e.clock.Advance(checkout.CreationRetryDelay)
	second := dispatchCreation(ctx, t, executor, 1)
	if second["t1"] >= hungCallBudget/2 {
		t.Fatal("при открытом размыкателе место полосы занято на срок вызова", second)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range state.Groups {
		if (g.OrderID != "") != (g.TenantID == "t2") {
			t.Fatal("заказ независшего магазина не создан или создан у зависшего", g)
		}
	}
}

// Аренда задачи не короче срока обработчика с запасом: иначе задачу, пока идёт запись итога,
// возьмёт второй исполнитель и повторит вызов магазина.
func TestExecutorLeaseCoversHandlerDeadline(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	queue := &leaseQueue{TaskQueue: e.store}
	uow := &deadlineUOW{UnitOfWork: e.store}
	svc := appcheckout.NewService(uow, e.platform, nil, nil, &fake.IDs{}, e.clock, appcheckout.Config{})
	executor := appcheckout.NewExecutor(queue, e.clock, svc, messaging.DefaultMaxAttempts)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	executor.Wait()
	uow.mu.Lock()
	defer uow.mu.Unlock()
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(uow.bounded) == 0 || len(queue.leases) == 0 || e.platform.OrderCount() == 0 {
		t.Fatal("создание заказа не выполнено", uow.bounded, queue.leases)
	}
	for n, bounded := range uow.bounded {
		if !bounded {
			t.Fatal("запись обработчика без срока может пережить аренду задачи", n)
		}
		if queue.leases[0] < 2*uow.remaining[n] {
			t.Fatal("аренда короче двух сроков обработчика", queue.leases[0], uow.remaining[n])
		}
	}
}

func (p *hungShopOrders) CreateOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	if tenant == p.tenant {
		return p.PlatformAPI.CreateOrder(context.WithValue(ctx, hangKey{}, true), tenant, request)
	}
	return p.PlatformAPI.CreateOrder(ctx, tenant, request)
}

func (q *leaseQueue) Claim(ctx context.Context, request ports.ClaimRequest) (ports.ClaimResult, error) {
	q.mu.Lock()
	q.leases = append(q.leases, request.LeaseUntil.Sub(request.Now))
	q.mu.Unlock()
	return q.TaskQueue.Claim(ctx, request)
}

func (u *deadlineUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	deadline, bounded := ctx.Deadline()
	u.mu.Lock()
	u.bounded = append(u.bounded, bounded)
	u.remaining = append(u.remaining, time.Until(deadline))
	u.mu.Unlock()
	return u.UnitOfWork.Do(ctx, fn)
}

// hungGuard — ограждение процесса над фейком; все обращения не отвечают до конца срока.
// Порог 1: одно засчитанное зависание открывает размыкатель магазина.
func hungGuard(e *env) (*platformguard.Client, *fake.PlatformMetrics) {
	metrics := &fake.PlatformMetrics{}
	guard := platformguard.New(
		e.platform,
		platformguard.Config{
			StrictTimeout:         hungCallBudget,
			MaxConcurrent:         16,
			CheckoutMaxConcurrent: 8,
			ReadMaxConcurrent:     8,
			TenantMaxConcurrent:   8,
			QueueSize:             32,
			TenantQueueSize:       8,
			TenantStateSize:       100,
			TenantIdleTTL:         time.Minute,
			FailureThreshold:      1,
			FailureWindow:         30 * time.Second,
			FailureMinCalls:       20,
			FailurePercent:        50,
			OpenInterval:          5 * time.Second,
		},
		e.clock,
		metrics,
		nil,
	)
	return guard, metrics
}

// hang включает зависание операций после того, как сценарий подготовлен обычным фейком.
func hang(e *env, operations ...string) {
	e.platform.CallHook = func(ctx context.Context, operation string) error {
		hung, _ := ctx.Value(hangKey{}).(bool)
		for _, candidate := range operations {
			hung = hung || candidate == operation
		}
		if hung {
			<-ctx.Done()
		}
		return ctx.Err()
	}
}

func guardedConfig() appcheckout.Config {
	return appcheckout.Config{
		QuoteTTL:         30 * time.Minute,
		QuoteTimeout:     hungCallBudget,
		OrderCallTimeout: hungCallBudget,
		ReturnBaseURL:    "https://market.example",
		PaymentStub:      true,
	}
}

func guardedService(e *env, guard *platformguard.Client) *appcheckout.Service {
	return appcheckout.NewService(e.store, guard, e.pickups, e.verifier, &fake.IDs{}, e.clock, guardedConfig())
}

func requireRejectedAtOnce(t *testing.T, guard *platformguard.Client, tenant ids.TenantID) {
	t.Helper()
	started := time.Now()
	_, err := guard.MarketplaceSettings(context.Background(), tenant)
	if !errors.Is(err, platformguard.ErrUnavailable) || time.Since(started) >= hungCallBudget/2 {
		t.Fatal("размыкатель магазина не открылся после зависания", tenant, err, time.Since(started))
	}
}

func hungCreate(t *testing.T, e *env, svc *appcheckout.Service) ids.TenantID {
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	task := firstCreationTask(t, e)
	hang(e, "create_order")
	if err := svc.HandleOrderTask(ctx, task); !appcheckout.CreationPending(err) {
		t.Fatal("зависание должно оставить повтор создания", err)
	}
	return task.TenantID
}

func hungPoll(t *testing.T, e *env, svc *appcheckout.Service) ids.TenantID {
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	due := state.Groups[0].ExpiresAt.Add(checkout.PaymentExpiryGrace)
	e.clock.Advance(due.Sub(e.clock.Now()))
	if err := e.service.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	hang(e, "get_order")
	if err := runTasks(ctx, e, svc); err != nil {
		t.Fatal(err)
	}
	return state.Groups[0].TenantID
}

func hungCancel(t *testing.T, e *env, svc *appcheckout.Service) ids.TenantID {
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Cancel(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	hang(e, "cancel_order")
	if err := runTasks(ctx, e, svc); err != nil {
		t.Fatal(err)
	}
	return state.Groups[0].TenantID
}

func hungStubPayment(t *testing.T, e *env, svc *appcheckout.Service) ids.TenantID {
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	hang(e, "stub_payment")
	if err := svc.ConfirmPayment(ctx, buyerID, c.ID); err == nil {
		t.Fatal("оплата у зависшего магазина прошла")
	}
	return state.Groups[0].TenantID
}

func hungQuote(t *testing.T, e *env, svc *appcheckout.Service) ids.TenantID {
	ctx := context.Background()
	state, err := e.service.Create(ctx, buyerID, key)
	if err != nil {
		t.Fatal(err)
	}
	hang(e, "quote")
	if _, err := svc.Quote(ctx, buyerID, state.ID, "cdek", "MSK1", "Город"); err != nil {
		t.Log("расчёт при зависшем магазине:", err)
	}
	return state.Groups[0].TenantID
}

// dispatchCreation запускает один проход полосы создания и возвращает время каждой задачи по магазинам.
func dispatchCreation(
	ctx context.Context,
	t *testing.T,
	executor *appcheckout.Executor,
	tasks int,
) map[ids.TenantID]time.Duration {
	t.Helper()
	started := time.Now()
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	took := make(map[ids.TenantID]time.Duration)
	for range tasks {
		select {
		case result := <-executor.Results():
			took[result.Task.TenantID] = time.Since(started)
		case <-time.After(10 * hungCallBudget):
			t.Fatal("задача создания не завершилась", took)
		}
	}
	return took
}
