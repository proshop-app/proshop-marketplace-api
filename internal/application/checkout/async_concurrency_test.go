package checkout_test

import (
	"context"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type delayedOrderPlatform struct {
	ports.PlatformAPI
	release <-chan struct{}
	started chan<- struct{}
}

type delayedPollPlatform struct {
	ports.PlatformAPI
	release <-chan struct{}
	started chan<- struct{}
}

func (p delayedPollPlatform) GetOrder(
	ctx context.Context,
	tenant ids.TenantID,
	orderID string,
) (checkout.SellerOrder, error) {
	p.started <- struct{}{}
	select {
	case <-p.release:
	case <-ctx.Done():
		return checkout.SellerOrder{}, ctx.Err()
	}
	return p.PlatformAPI.GetOrder(ctx, tenant, orderID)
}

func (p delayedOrderPlatform) CreateOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	if tenant == "t1" {
		p.started <- struct{}{}
		select {
		case <-p.release:
		case <-ctx.Done():
			return checkout.SellerOrder{}, ctx.Err()
		}
	}
	return p.PlatformAPI.CreateOrder(ctx, tenant, request)
}

func TestCreationExecutorDoesNotWaitForSlowSellerOrOtherTask(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	platform := delayedOrderPlatform{PlatformAPI: e.platform, release: release, started: started}
	service := appcheckout.NewService(e.store, e.index, platform, nil, nil, &fake.IDs{}, e.clock, appcheckout.Config{})
	executor := appcheckout.NewCreationExecutor(e.store, e.clock, service)
	otherStarted := make(chan struct{})
	otherRelease := make(chan struct{})
	otherDone := make(chan error, 1)
	if err := e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().Enqueue(ctx, ports.NewTask{Kind: "slow_other", TenantID: "t1", At: e.clock.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher := messaging.NewDispatcher(
		e.store,
		e.clock,
		messaging.DispatcherConfig{
			Batch:     200,
			PerTenant: 200,
			Lease:     time.Minute,
			ExcludeKinds: []string{
				appcheckout.TaskCreateOrder,
				appcheckout.TaskExpireCreation,
				appcheckout.TaskPollOrder,
			},
		},
		map[string]messaging.Handler{
			"slow_other": func(context.Context, ports.Task) error {
				close(otherStarted)
				<-otherRelease
				return nil
			},
		},
	)
	go func() { _, err := dispatcher.RunOnce(ctx); otherDone <- err }()
	<-otherStarted
	defer func() {
		close(release)
		close(otherRelease)
		executor.Wait()
		late, err := service.Get(ctx, buyerID, c.ID)
		if err != nil || late.Groups[0].OrderID == "" || late.Groups[0].Status != checkout.GroupUnconfirmed {
			t.Error("поздний заказ сохраняется для сверки", late, err)
		}
		if err := <-otherDone; err != nil {
			t.Error(err)
		}
	}()
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	<-started
	for _, row := range e.store.OutboxRows() {
		if row.Kind == appcheckout.TaskCreateOrder && row.TenantID == "t1" &&
			row.NextAt.Sub(e.clock.Now()) < appcheckout.CreationCallTimeout+3*time.Second {
			t.Fatal("аренда задачи должна оставлять время на запись итога", row)
		}
	}
	result := <-executor.Results()
	if result.Err != nil || result.Task.TenantID != "t2" {
		t.Fatal(result)
	}
	state, err := service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[1].OrderID == "" || state.Groups[0].OrderID != "" {
		t.Fatal("здоровый магазин должен завершиться независимо", state, err)
	}
	// Закрытие срока не занимает место HTTP и не ждёт заблокированный магазин.
	e.clock.Advance(appcheckout.CreationTimeout)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	for closed := 0; closed < 2; {
		result := <-executor.Results()
		if result.Err != nil {
			t.Fatal(result)
		}
		if result.Task.Kind == appcheckout.TaskExpireCreation {
			closed++
		}
	}
	state, err = service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].Status != checkout.GroupUnconfirmed ||
		!state.Groups[0].ReconciliationRequired || !state.Groups[0].CartReturned {
		t.Fatal(state, err)
	}

}

func TestPollLaneDoesNotBlockCreationOrGeneralOutbox(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	task := firstCreationTask(t, e)
	if err := e.service.HandleOrderTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Complete(ctx, task.ID, task.Attempts, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.service.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}, 2), make(chan struct{})
	platform := delayedPollPlatform{PlatformAPI: e.platform, release: release, started: started}
	service := appcheckout.NewService(e.store, e.index, platform, nil, nil, &fake.IDs{}, e.clock, appcheckout.Config{})
	executor := appcheckout.NewCreationExecutor(e.store, e.clock, service)
	defer func() { close(release); executor.Wait() }()
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	<-started
	result := <-executor.Results()
	if result.Err != nil || result.Task.Kind != appcheckout.TaskCreateOrder || result.Task.TenantID != "t2" {
		t.Fatal("сверка задержала создание", result)
	}
	// Дополнительная сверка того же магазина должна остаться в очереди при занятом лимите.
	rows := e.store.OutboxRows()
	if err := e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for _, row := range rows {
			if row.Kind == appcheckout.TaskPollOrder {
				row.DedupKey += ":again"
				if err := tx.Outbox().Enqueue(ctx, ports.NewTask{
					Kind:     row.Kind,
					TenantID: row.TenantID,
					DedupKey: row.DedupKey,
					Payload:  row.Payload,
					At:       e.clock.Now(),
				}); err != nil {
					return err
				}
			}
		}
		return tx.Outbox().Enqueue(ctx, ports.NewTask{Kind: "index", TenantID: "t1", At: e.clock.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	counts, err := e.store.ReadyCounts(ctx, e.clock.Now())
	if err != nil || counts[appcheckout.TaskPollOrder] != 1 {
		t.Fatal("лишняя сверка должна остаться без аренды", counts, err)
	}
	dispatcher := messaging.NewDispatcher(
		e.store,
		e.clock,
		messaging.DispatcherConfig{
			Batch:     200,
			PerTenant: 200,
			Lease:     time.Minute,
			ExcludeKinds: []string{
				appcheckout.TaskCreateOrder,
				appcheckout.TaskExpireCreation,
				appcheckout.TaskPollOrder,
			},
		},
		map[string]messaging.Handler{"index": func(context.Context, ports.Task) error { return nil }},
	)
	outcomes, err := dispatcher.RunOnce(ctx)
	if err != nil || len(outcomes) != 1 || outcomes[0].Err != nil || outcomes[0].Task.Kind != "index" {
		t.Fatal("сверка задержала общий outbox", outcomes, err)
	}
	e.clock.Advance(appcheckout.CreationTimeout)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result := <-executor.Results()
		if result.Err != nil || result.Task.Kind != appcheckout.TaskExpireCreation {
			t.Fatal(result)
		}
	}
}
