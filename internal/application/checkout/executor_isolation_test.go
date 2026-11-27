package checkout_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/platformguard"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type timedTaskQueue struct {
	*leaseQueue
	release     <-chan struct{}
	claims      chan time.Time
	resolutions chan time.Time
}

func (q timedTaskQueue) Claim(ctx context.Context, request ports.ClaimRequest) (ports.ClaimResult, error) {
	if q.claims != nil {
		deadline, _ := ctx.Deadline()
		q.claims <- deadline
	}
	return q.leaseQueue.Claim(ctx, request)
}

func (q timedTaskQueue) Resolve(ctx context.Context, resolutions []ports.TaskResolution) error {
	if q.resolutions != nil {
		deadline, _ := ctx.Deadline()
		q.resolutions <- deadline
	}
	if q.release != nil {
		select {
		case <-q.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return q.TaskQueue.Resolve(ctx, resolutions)
}

func TestExecutorSkipsCreationWhileServerIsOpen(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	const budget = time.Second
	started := make(chan struct{}, appcheckout.CreationWorkers)
	release := make(chan struct{})
	var upstreamCalls atomic.Int32
	e.platform.CallHook = func(_ context.Context, operation string) error {
		if operation != "create_order" {
			return context.Canceled
		}
		upstreamCalls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
			return context.DeadlineExceeded
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	guard := platformguard.New(
		e.platform,
		platformguard.Config{
			StrictTimeout:         budget,
			MaxConcurrent:         16,
			CheckoutMaxConcurrent: 8,
			ReadMaxConcurrent:     8,
			TenantMaxConcurrent:   8,
			TenantStateSize:       100,
			TenantIdleTTL:         time.Minute,
			FailureThreshold:      5,
			FailureWindow:         30 * time.Second,
			FailureMinCalls:       len(c.Groups),
			FailurePercent:        50,
			OpenInterval:          5 * time.Second,
		},
		e.clock,
		nil,
		nil,
	)
	// Используем ту же обёртку, что и в сборке приложения.
	upstream := participant.NewResyncingPlatform(guard, e.store, e.clock)
	config := guardedConfig()
	config.OrderCallTimeout = appcheckout.CreationCallTimeout
	service := appcheckout.NewService(e.store, upstream, e.pickups, e.verifier, &fake.IDs{}, e.clock, config)
	queue := &leaseQueue{TaskQueue: e.store}
	executor := appcheckout.NewExecutor(queue, e.clock, service, guard, messaging.DefaultMaxAttempts)
	t.Cleanup(func() {
		cancel()
		executor.Wait()
	})
	// Срок обработчика начнётся позже срока покупки.
	e.clock.Advance(time.Second)
	if err := dispatchForTest(ctx, executor); err != nil {
		t.Fatal(err)
	}
	for range c.Groups {
		<-started
	}

	// Оба HTTP ещё висят, а срок покупки обрабатывается своей полосой.
	e.clock.Advance(appcheckout.CreationTimeout - time.Second)
	if err := dispatchForTest(ctx, executor); err != nil {
		t.Fatal(err)
	}
	for closed := 0; closed < len(c.Groups); {
		result := <-executor.Results()
		if result.Task.Kind == appcheckout.TaskCancelOrder {
			continue
		}
		if result.Task.Kind != appcheckout.TaskExpireCreation || result.Err != nil {
			t.Fatal("закрытие срока дождалось общего сервера", result)
		}
		closed++
	}
	state, err := service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusDraft {
		t.Fatal("срок покупки не закрыт", state.Status, err)
	}
	close(release)
	for finished := 0; finished < len(c.Groups); {
		if result := <-executor.Results(); result.Task.Kind == appcheckout.TaskCreateOrder {
			finished++
		}
	}
	if guard.ServerState() != ports.PlatformServerBlocked {
		t.Fatal("массовое зависание не открыло размыкатель сервера")
	}
	enqueueExecutorTasks(t, e, []ports.NewTask{{
		Kind:     appcheckout.TaskCreateOrder,
		TenantID: "t3",
		Payload:  []byte(`{}`),
		At:       e.clock.Now(),
	}})
	before := queue.calls(appcheckout.TaskCreateOrder)
	for range 4 {
		e.clock.Advance(time.Second)
		if err := dispatchForTest(ctx, executor); err != nil {
			t.Fatal(err)
		}
	}
	if calls := queue.calls(appcheckout.TaskCreateOrder); calls != before {
		t.Fatal("при открытом размыкателе исполнитель брал создание", calls-before)
	}
	executor.Wait()
	if calls := upstreamCalls.Load(); int(calls) != len(c.Groups) {
		t.Fatal("после размыкания были новые вызовы сервера", calls)
	}
	for _, row := range e.store.OutboxRows() {
		if row.Kind == appcheckout.TaskCreateOrder && row.TenantID == "t3" && row.Attempts != 0 {
			t.Fatal("новая задача создания должна остаться без аренды", row)
		}
	}
}

func TestExecutorRefillsOnlyResultLaneAfterNewArrival(t *testing.T) {
	e := setup(t)
	now := e.clock.Now()
	enqueueExecutorTasks(t, e, []ports.NewTask{
		invalidCreation(now, "t1"),
		{
			Kind:        appcheckout.TaskExpireCreation,
			TenantID:    "t1",
			Payload:     []byte(`{}`),
			At:          now,
			AvailableAt: now.Add(time.Second),
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &leaseQueue{TaskQueue: e.store}
	executor := appcheckout.NewExecutor(queue, e.clock, e.service, nil, messaging.DefaultMaxAttempts)
	t.Cleanup(executor.Wait)
	if err := dispatchForTest(ctx, executor); err != nil {
		t.Fatal(err)
	}
	if queue.calls("") != 4 {
		t.Fatal("начальный проход должен проверить четыре полосы", queue.calls(""))
	}
	// После начального взятия в полосе ничего не оставалось. Новая задача
	// должна попасть к исполнителю уже на пополнении от первого результата.
	enqueueExecutorTasks(t, e, []ports.NewTask{invalidCreation(now, "t2")})
	for _, tenant := range []ids.TenantID{"t1", "t2"} {
		result := <-executor.Results()
		if result.Task.Kind != appcheckout.TaskCreateOrder || result.Task.TenantID != tenant {
			t.Fatal("ожидался результат создания для магазина", tenant, result)
		}
		before := queue.calls("")
		if _, _, err := executor.Refill(ctx, result.Task.Kind); err != nil {
			t.Fatal(err)
		}
		if got := queue.calls("") - before; got != 1 {
			t.Fatalf("результат %s: взятий %d, ожидалось одно", tenant, got)
		}
	}
	if queue.calls(appcheckout.TaskExpireCreation) != 1 {
		t.Fatal("результат создания вызвал взятие отложенной задачи срока")
	}
	e.clock.Advance(time.Second)
	if err := dispatchForTest(ctx, executor); err != nil {
		t.Fatal(err)
	}
	if result := <-executor.Results(); result.Task.Kind != appcheckout.TaskExpireCreation {
		t.Fatal("очередной проход не заметил созревшую задачу срока", result)
	}
}

func TestExecutorRefillsAfterFullLaneMissesNewWork(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	queue := timedTaskQueue{leaseQueue: &leaseQueue{TaskQueue: e.store}, release: release}
	executor := appcheckout.NewExecutor(queue, e.clock, e.service, nil, messaging.DefaultMaxAttempts)
	defer func() {
		cancel()
		executor.Wait()
	}()
	var tasks []ports.NewTask
	for n := range appcheckout.CreationWorkers + 1 {
		tasks = append(tasks, invalidCreation(e.clock.Now(), ids.TenantID(fmt.Sprintf("t%02d", n))))
	}
	enqueueExecutorTasks(t, e, tasks[:appcheckout.CreationWorkers])
	if err := dispatchForTest(ctx, executor); err != nil {
		t.Fatal(err)
	}
	enqueueExecutorTasks(t, e, tasks[appcheckout.CreationWorkers:])
	if err := dispatchForTest(ctx, executor); err != nil {
		t.Fatal(err)
	}
	if got := queue.calls(appcheckout.TaskCreateOrder); got != 1 {
		t.Fatal("заполненная полоса вызвала взятие", got)
	}
	close(release)
	result := <-executor.Results()
	if _, _, err := executor.Refill(ctx, result.Task.Kind); err != nil {
		t.Fatal(err)
	}
	if got := queue.calls(appcheckout.TaskCreateOrder); got != 2 || queue.taken() != len(tasks) {
		t.Fatal("первое свободное место не взяло новую задачу одним запросом", got, queue.taken())
	}
}

func TestExecutorProbeWaitsForActiveCreationAndTakesOneCandidate(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	queue := timedTaskQueue{
		leaseQueue:  &leaseQueue{TaskQueue: e.store},
		release:     release,
		resolutions: make(chan time.Time, 2),
	}
	cfg := executorGuardConfig()
	cfg.FailureThreshold = 1
	cfg.OpenInterval = time.Second
	guard := platformguard.New(e.platform, cfg, e.clock, nil, nil)
	executor := appcheckout.NewExecutor(queue, e.clock, e.service, guard, messaging.DefaultMaxAttempts)
	defer func() {
		cancel()
		executor.Wait()
	}()
	now := e.clock.Now()
	enqueueExecutorTasks(t, e, []ports.NewTask{invalidCreation(now, "t1")})
	if err := dispatchForTest(ctx, executor); err != nil {
		t.Fatal(err)
	}
	<-queue.resolutions
	e.platform.Fail("t2", context.DeadlineExceeded)
	_, _ = guard.MarketplaceSettings(ctx, "t2")
	e.platform.Fail("t3", &ports.PlatformServerFailure{Cause: errors.New("отказ сервера")})
	_, _ = guard.MarketplaceSettings(ctx, "t3")
	e.clock.Advance(cfg.OpenInterval)
	enqueueExecutorTasks(t, e, []ports.NewTask{
		invalidCreation(now, "t4"),
		invalidCreation(now, "t5"),
		invalidCreation(now.Add(time.Second), "t2"),
	})
	if got := guard.ServerState(); got != ports.PlatformServerProbe {
		t.Fatal("после паузы нужна проба", got)
	}
	if err := dispatchForTest(ctx, executor); err != nil {
		t.Fatal(err)
	}
	if got := queue.calls(appcheckout.TaskCreateOrder); got != 1 {
		t.Fatal("проба запущена при активной задаче", got)
	}
	close(release)
	result := <-executor.Results()
	if _, _, err := executor.Refill(ctx, result.Task.Kind); err != nil {
		t.Fatal(err)
	}
	if got := queue.taken(); got != 2 {
		t.Fatal("на пробу нужна ровно одна новая задача", got)
	}
	if result := <-executor.Results(); result.Task.TenantID != "t5" {
		t.Fatal("проба должна пропустить магазин с недавним отказом", result)
	}
}

func TestExecutorBoundsClaimAndResolve(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	queue := timedTaskQueue{
		leaseQueue:  &leaseQueue{TaskQueue: e.store},
		claims:      make(chan time.Time, 4),
		resolutions: make(chan time.Time, 1),
	}
	executor := appcheckout.NewExecutor(queue, e.clock, e.service, nil, messaging.DefaultMaxAttempts)
	enqueueExecutorTasks(t, e, []ports.NewTask{invalidCreation(e.clock.Now(), "t1")})
	want := e.clock.Now().Add(5 * time.Second)
	if err := dispatchForTest(ctx, executor); err != nil {
		t.Fatal(err)
	}
	<-executor.Results()
	for _, deadlines := range []chan time.Time{queue.claims, queue.resolutions} {
		close(deadlines)
		for deadline := range deadlines {
			if !deadline.Equal(want) {
				t.Fatal("взятие и отметка должны иметь бюджет пять секунд", deadline)
			}
		}
	}
}

func invalidCreation(at time.Time, tenant ids.TenantID) ports.NewTask {
	return ports.NewTask{Kind: appcheckout.TaskCreateOrder, TenantID: tenant, Payload: []byte("["), At: at}
}

func enqueueExecutorTasks(t *testing.T, e *env, tasks []ports.NewTask) {
	t.Helper()
	if err := e.store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().EnqueueBatch(ctx, tasks)
	}); err != nil {
		t.Fatal(err)
	}
}
