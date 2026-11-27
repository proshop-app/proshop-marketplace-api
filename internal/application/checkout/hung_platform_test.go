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

// enteringOrders сообщает, что обработчик дошёл до вызова создания заказа через ограждение.
type enteringOrders struct {
	ports.PlatformAPI
	entered chan struct{}
}

// leaseQueue запоминает аренду, которую исполнитель просит у очереди.
type leaseQueue struct {
	ports.TaskQueue
	mu      sync.Mutex
	leases  []time.Duration
	claimed int
	kinds   []string
}

// deadlineUOW запоминает срок контекста каждой транзакции обработчика.
type deadlineUOW struct {
	ports.UnitOfWork
	clock     ports.Clock
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
	guard, metrics := hungGuard(e)
	started := make(chan struct{}, 1)
	e.platform.CallHook = func(ctx context.Context, _ string) error {
		hung, _ := ctx.Value(hangKey{}).(bool)
		if hung {
			started <- struct{}{}
			<-ctx.Done()
		}
		return ctx.Err()
	}
	upstream := &hungShopOrders{PlatformAPI: guard, tenant: "t1"}
	svc := appcheckout.NewService(e.store, upstream, e.pickups, e.verifier, &fake.IDs{}, e.clock, guardedConfig())
	executor := appcheckout.NewExecutor(e.store, e.clock, svc, guard, messaging.DefaultMaxAttempts)
	if err := dispatchForTest(ctx, executor); err != nil {
		t.Fatal(err)
	}
	<-started
	if first := <-executor.Results(); first.Task.TenantID != "t2" || first.Err != nil {
		t.Fatal("здоровый магазин должен создать заказ до завершения зависшего вызова", first)
	}
	e.clock.Advance(hungCallBudget)
	if hung := <-executor.Results(); hung.Task.TenantID != "t1" || !appcheckout.CreationPending(hung.Err) {
		t.Fatal("у зависшего магазина должен остаться повтор", hung)
	}
	e.clock.Advance(checkout.CreationRetryDelay)
	if err := dispatchForTest(ctx, executor); err != nil {
		t.Fatal(err)
	}
	if second := <-executor.Results(); second.Task.TenantID != "t1" || !appcheckout.CreationPending(second.Err) {
		t.Fatal("открытый размыкатель должен отложить повтор", second)
	}
	if metrics.Count("create_order", "timeout") != 1 || metrics.Count("create_order", "tenant_breaker") != 1 {
		t.Fatal("повтор при открытом размыкателе дошёл до сервера")
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
	uow := &deadlineUOW{UnitOfWork: e.store, clock: e.clock}
	svc := appcheckout.NewService(uow, e.platform, nil, nil, &fake.IDs{}, e.clock, appcheckout.Config{})
	executor := appcheckout.NewExecutor(queue, e.clock, svc, nil, messaging.DefaultMaxAttempts)
	if err := dispatchForTest(ctx, executor); err != nil {
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

// При открытом размыкателе задачи создания ждут его допуска: за интервал — ни одного взятия
// и транзакции вместо повтора каждую секунду. Допуск после срока создания — задача завершается,
// группу закроет задача срока.
func TestOpenBreakerDefersCreationUntilReopen(t *testing.T) {
	for _, open := range []time.Duration{5 * time.Second, 20 * time.Second} {
		t.Run(open.String(), func(t *testing.T) {
			e := setup(t)
			c := confirmedPurchase(t, e)
			ctx := context.Background()
			if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
				t.Fatal(err)
			}
			guard, _ := hungGuardOpen(e, open)
			for _, group := range c.Groups {
				e.platform.FailCreate(group.TenantID, context.DeadlineExceeded)
			}
			queue := &leaseQueue{TaskQueue: e.store}
			uow := &deadlineUOW{UnitOfWork: e.store, clock: e.clock}
			svc := appcheckout.NewService(uow, guard, e.pickups, e.verifier, &fake.IDs{}, e.clock, guardedConfig())
			executor := appcheckout.NewExecutor(queue, e.clock, svc, guard, messaging.DefaultMaxAttempts)
			if taken := dispatchDue(ctx, t, executor, queue); taken != len(c.Groups) {
				t.Fatal("первый проход не взял задачи создания", taken)
			}
			// Штатный повтор через 1 с после зависания узнаёт «не раньше»; дальше задачи ждут допуска.
			transactions, taken := uow.transactions(), 0
			waited := min(open, appcheckout.CreationTimeout) - time.Second
			for step := time.Duration(0); step < waited; step += time.Second / 2 {
				e.clock.Advance(time.Second / 2)
				taken += dispatchDue(ctx, t, executor, queue)
			}
			spent := uow.transactions() - transactions
			t.Log("взятий за интервал:", taken, "транзакций:", spent)
			if taken > len(c.Groups) || spent > 4*len(c.Groups) {
				t.Fatal("задачи создания крутятся при открытом размыкателе", taken, spent)
			}
			afterDeadline := open > appcheckout.CreationTimeout
			for _, row := range e.store.OutboxRows() {
				if row.Kind == appcheckout.TaskCreateOrder && (row.DoneAt != nil) != afterDeadline {
					t.Fatal("задача создания: допуск после срока завершает её, до срока — откладывает", row)
				}
			}
		})
	}
}

// Отмена при открытом размыкателе тоже ждёт допуска, а не нарастающей паузы с первых секунд.
func TestOpenBreakerDefersCancellationUntilReopen(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Cancel(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	guard, _ := hungGuard(e)
	for _, group := range c.Groups {
		e.platform.FailCancel(group.TenantID, context.DeadlineExceeded)
	}
	queue := &leaseQueue{TaskQueue: e.store}
	svc := guardedService(e, guard)
	executor := appcheckout.NewExecutor(queue, e.clock, svc, guard, messaging.DefaultMaxAttempts)
	if taken := dispatchDue(ctx, t, executor, queue); taken != len(c.Groups) {
		t.Fatal("первый проход не взял задачи отмены", taken)
	}
	taken := 0
	for step := time.Duration(0); step < 4*time.Second; step += time.Second / 2 {
		e.clock.Advance(time.Second / 2)
		taken += dispatchDue(ctx, t, executor, queue)
	}
	t.Log("взятий отмены за интервал:", taken)
	if taken > len(c.Groups) {
		t.Fatal("задачи отмены крутятся при открытом размыкателе", taken)
	}
}

// Создание, дождавшееся места к сроку создания, не вызывает сервер: заказ всё равно закроет
// задача срока, а заголовок срока обещал бы серверу время, которого у площадки нет.
func TestCreationAdmittedAtDeadlineSkipsServer(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	task := firstCreationTask(t, e)
	occupied, release := make(chan struct{}), make(chan struct{})
	var created sync.WaitGroup
	e.platform.CallHook = func(ctx context.Context, operation string) error {
		switch operation {
		case "quote":
			close(occupied)
			<-release
		case "create_order":
			t.Error("сервер вызван после срока создания")
		}
		return ctx.Err()
	}
	metrics := &fake.PlatformMetrics{}
	guard := platformguard.New(
		e.platform,
		platformguard.Config{
			StrictTimeout:         time.Second,
			MaxConcurrent:         2,
			CheckoutMaxConcurrent: 1,
			ReadMaxConcurrent:     1,
			TenantMaxConcurrent:   2,
			QueueSize:             8,
			TenantQueueSize:       4,
			TenantStateSize:       100,
			TenantIdleTTL:         time.Minute,
			FailureThreshold:      1,
			FailureWindow:         30 * time.Second,
			FailureMinCalls:       1,
			FailurePercent:        1,
			OpenInterval:          5 * time.Second,
		},
		e.clock,
		metrics,
		nil,
	)
	upstream := &enteringOrders{PlatformAPI: guard, entered: make(chan struct{}, 1)}
	svc := appcheckout.NewService(e.store, upstream, e.pickups, e.verifier, &fake.IDs{}, e.clock, guardedConfig())
	checkoutCtx := ports.WithPlatformCallClass(ctx, ports.PlatformCheckout)
	quoteCtx, cancelQuote := context.WithCancel(checkoutCtx)
	defer cancelQuote()
	created.Add(1)
	go func() {
		defer created.Done()
		_, _ = guard.QuoteDelivery(quoteCtx, "t9", ports.QuoteRequest{Carrier: "cdek", City: "Город"})
	}()
	<-occupied
	handled := make(chan error, 1)
	go func() { handled <- svc.HandleOrderTask(ctx, task) }()
	// Задача захватила группу и ждёт место; время доходит до срока создания, место освобождается.
	<-upstream.entered
	cancelQuote()
	e.clock.Advance(appcheckout.CreationTimeout)
	close(release)
	err := <-handled
	created.Wait()
	// Срок мог наступить до постановки в очередь или после получения места — сервер не вызван в обоих.
	if metrics.Count("create_order", "deadline_passed")+metrics.Count("create_order", "queue_timeout") != 1 {
		t.Fatal("создание после срока не отклонено без вызова сервера", err)
	}
	requireBreakersClosed(t, guard, task.TenantID)
}

func (p *enteringOrders) CreateOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	p.entered <- struct{}{}
	return p.PlatformAPI.CreateOrder(ctx, tenant, request)
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
	result, err := q.TaskQueue.Claim(ctx, request)
	q.mu.Lock()
	q.leases = append(q.leases, request.LeaseUntil.Sub(request.Now))
	q.claimed += len(result.Tasks)
	q.kinds = append(q.kinds, request.Kinds...)
	q.mu.Unlock()
	return result, err
}

func (q *leaseQueue) calls(kind string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	count := 0
	for _, called := range q.kinds {
		if kind == "" || called == kind {
			count++
		}
	}
	return count
}

func (q *leaseQueue) taken() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.claimed
}

func (u *deadlineUOW) transactions() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.bounded)
}

func (u *deadlineUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	deadline, bounded := ctx.Deadline()
	u.mu.Lock()
	u.bounded = append(u.bounded, bounded)
	u.remaining = append(u.remaining, deadline.Sub(u.clock.Now()))
	u.mu.Unlock()
	return u.UnitOfWork.Do(ctx, fn)
}

// hungGuard — ограждение процесса над фейком; все обращения не отвечают до конца срока.
// Порог 1: одно засчитанное зависание открывает размыкатель магазина.
func hungGuard(e *env) (*platformguard.Client, *fake.PlatformMetrics) {
	return hungGuardOpen(e, 5*time.Second)
}

func hungGuardOpen(e *env, open time.Duration) (*platformguard.Client, *fake.PlatformMetrics) {
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
			OpenInterval:          open,
		},
		e.clock,
		metrics,
		nil,
	)
	return guard, metrics
}

// hang включает зависание операций после того, как сценарий подготовлен обычным фейком.
func hang(e *env, target string) {
	e.platform.CallHook = func(ctx context.Context, operation string) error {
		if operation == target {
			return e.clock.Expire(ctx)
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
	_, err := guard.MarketplaceSettings(context.Background(), tenant)
	var rejected *ports.PlatformRejected
	if !errors.As(err, &rejected) || rejected.Server {
		t.Fatal("размыкатель магазина не открылся после зависания", tenant, err)
	}
}

func requireBreakersClosed(t *testing.T, guard *platformguard.Client, tenant ids.TenantID) {
	t.Helper()
	if _, err := guard.MarketplaceSettings(context.Background(), tenant); err != nil {
		t.Fatal("отказ без вызова сервера засчитан магазину или серверу", err)
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
	started := make(chan struct{}, len(state.Groups))
	e.platform.CallHook = func(ctx context.Context, operation string) error {
		if operation == "quote" {
			started <- struct{}{}
			<-ctx.Done()
		}
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, err := svc.Quote(ctx, buyerID, state.ID, "cdek", "MSK1", "Город")
		done <- err
	}()
	for range state.Groups {
		<-started
	}
	e.clock.Advance(hungCallBudget)
	if err := <-done; err != nil {
		t.Log("расчёт при зависшем магазине:", err)
	}
	return state.Groups[0].TenantID
}

// dispatchDue делает один проход исполнителя и ждёт итогов всех взятых задач.
func dispatchDue(ctx context.Context, t *testing.T, executor *appcheckout.Executor, queue *leaseQueue) int {
	t.Helper()
	before := queue.taken()
	if err := dispatchForTest(ctx, executor); err != nil {
		t.Fatal(err)
	}
	taken := queue.taken() - before
	for range taken {
		<-executor.Results()
	}
	return taken
}
