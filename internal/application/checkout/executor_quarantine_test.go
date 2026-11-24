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
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// classQueue запоминает класс базы, с которым исполнитель обращается к очереди.
type classQueue struct {
	ports.TaskQueue
	mu      sync.Mutex
	classes []ports.DatabaseClass
}

// panickingOrderPlatform падает паникой на создании заказа магазина.
type panickingOrderPlatform struct {
	ports.PlatformAPI
}

func TestExecutorQuarantinesMalformedTasks(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	err := e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().Enqueue(ctx, ports.NewTask{
			Kind:     appcheckout.TaskCreateOrder,
			TenantID: "t1",
			Payload:  []byte("{"),
			At:       e.clock.Now(),
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := appcheckout.NewExecutor(e.store, e.clock, e.service, messaging.DefaultMaxAttempts)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.Results():
	case <-time.After(time.Second):
		t.Fatal("некорректная задача не разобрана")
	}
	executor.Wait()
	e.clock.Advance(24 * time.Hour)
	tasks, err := e.store.Claim(ctx, ports.ClaimRequest{
		Now:        e.clock.Now(),
		Limit:      1,
		PerTenant:  1,
		LeaseUntil: e.clock.Now().Add(time.Minute),
	})
	if err != nil || len(tasks) != 0 {
		t.Fatal("полоса оформления повторяет постоянную ошибку", tasks, err)
	}
}

func TestExecutorShutdownLeavesInFlightTaskForNextProcess(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 2)
	platform := delayedOrderPlatform{PlatformAPI: e.platform, release: make(chan struct{}), started: started}
	service := appcheckout.NewService(e.store, platform, nil, nil, &fake.IDs{}, e.clock, appcheckout.Config{})
	executor := appcheckout.NewExecutor(e.store, e.clock, service, 1)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("вызов магазина не начался")
	}
	cancel()
	executor.Wait()
	var inFlight int64
	for _, row := range e.store.OutboxRows() {
		if row.Kind == appcheckout.TaskCreateOrder && row.TenantID == "t1" {
			inFlight = row.ID
			if row.FailedAt != nil || row.DoneAt != nil || row.UnknownFailures != 0 {
				t.Fatal("отмена процесса завершила задачу", row)
			}
		}
	}
	if inFlight == 0 {
		t.Fatal("не найдена задача в полёте")
	}
	e.clock.Advance(time.Minute)
	resumed := appcheckout.NewExecutor(e.store, e.clock, e.service, 1)
	if err := resumed.Dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	resumed.Wait()
	for _, row := range e.store.OutboxRows() {
		if row.ID == inFlight && (row.DoneAt == nil || row.FailedAt != nil) {
			t.Fatal("следующий процесс не восстановил задачу", row)
		}
	}
}

func (q *classQueue) Claim(ctx context.Context, request ports.ClaimRequest) ([]ports.Task, error) {
	q.remember(ctx)
	return q.TaskQueue.Claim(ctx, request)
}

func (q *classQueue) Resolve(ctx context.Context, results []ports.TaskResolution) error {
	q.remember(ctx)
	return q.TaskQueue.Resolve(ctx, results)
}

func (q *classQueue) remember(ctx context.Context) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.classes = append(q.classes, ports.DatabaseClassFrom(ctx))
}

func (panickingOrderPlatform) CreateOrder(
	context.Context,
	ids.TenantID,
	ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	panic("сбой разбора ответа магазина")
}

func TestExecutorQuarantinesPanickingTask(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	service := appcheckout.NewService(
		e.store,
		panickingOrderPlatform{PlatformAPI: e.platform},
		nil,
		nil,
		&fake.IDs{},
		e.clock,
		appcheckout.Config{},
	)
	executor := appcheckout.NewExecutor(e.store, e.clock, service, 1)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	var result appcheckout.TaskResult
	select {
	case result = <-executor.Results():
	case <-time.After(time.Second):
		t.Fatal("паника исполнителя не дала итога задачи")
	}
	executor.Wait()
	if !errors.Is(result.Err, errs.ErrPanic) || result.FailureReason != "attempts_exhausted" {
		t.Fatalf("паника — неизвестный сбой с исчерпанием попыток: %+v", result)
	}
	for _, row := range e.store.OutboxRows() {
		if row.ID == result.Task.ID && row.FailedAt == nil {
			t.Fatal("задача с паникой не ушла в карантин", row)
		}
	}
}

func TestExecutorUsesOrdersDatabaseClass(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	queue := &classQueue{TaskQueue: e.store}
	executor := appcheckout.NewExecutor(queue, e.clock, e.service, messaging.DefaultMaxAttempts)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.Results():
	case <-time.After(time.Second):
		t.Fatal("задача создания не разобрана")
	}
	executor.Wait()
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.classes) < 2 {
		t.Fatal("взятие и отметка задач не дошли до очереди", queue.classes)
	}
	for _, class := range queue.classes {
		if class != ports.DatabaseOrders {
			t.Fatal("исполнитель оформления работает в своём пуле, а не в пуле фона", queue.classes)
		}
	}
}
