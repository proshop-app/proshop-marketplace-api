package checkout_test

import (
	"context"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

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
	executor := appcheckout.NewExecutor(e.store, e.clock, e.service)
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
