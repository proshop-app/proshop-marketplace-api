//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type rollbackCheckoutUOW struct {
	ports.UnitOfWork
	failure error
	calls   int
}

func (u *rollbackCheckoutUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	u.calls++
	return u.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		if u.calls > 2 {
			return u.failure
		}
		return nil
	})
}

func TestCheckoutServiceSchedulesDeadlineAndPreservesCAS(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedRecommendationProducts(t, uow, "t1", 1)
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	seedBuyer(t, uow, buyerA, "79251234567")
	ctx := context.Background()
	clock, platform := fake.NewClock(at(0)), fake.NewPlatform()
	platform.OrderNow = clock.Now()
	basket := cart.New("20000000-0000-4000-8000-000000000001", buyerA, clock.Now(), time.Hour)
	offer := cart.Offer{
		ID:        ids.OfferID{Tenant: "t1", Product: "p1"},
		Name:      "Товар",
		Price:     money.FromKopecks(100),
		Available: 1,
		Visible:   true,
	}
	if err := basket.Add(offer, 1, clock.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Carts().Save(ctx, basket)
	}); err != nil {
		t.Fatal(err)
	}

	c, err := checkout.New(
		"10000000-0000-4000-8000-000000000001",
		buyerA,
		"async-integration",
		[]checkout.Line{
			{
				Offer:    ids.OfferID{Tenant: "t1", Product: "p1"},
				Name:     "Товар",
				Quantity: 1,
				Price:    money.FromKopecks(100),
			},
		},
		clock.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SelectDelivery(
		checkout.Selection{
			Carrier:         "cdek",
			PickupPointCode: "P1",
			RegionCode:      "RU-MOW",
		},
		map[ids.TenantID]delivery.Quote{
			"t1": {Status: delivery.QuoteAvailable, ExpiresAt: at(60)},
		},
		clock.Now(),
	); err != nil {
		t.Fatal(err)
	}
	if err := c.Confirm(clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, c)
	}); err != nil {
		t.Fatal(err)
	}
	platform.SetSettings("t1", ports.ShopSettings{

		Delivery: participant.DeliverySettings{Carriers: []string{"cdek", "yandex"}, Regions: []string{"RU-MOW"}},
	})
	platform.SetQuote("t1", delivery.Offered{Available: true})
	cfg := appcheckout.Config{PaymentStub: true, QuoteTimeout: time.Second, QuoteTTL: time.Hour}
	service := appcheckout.NewService(
		uow,
		platform,
		nil,
		nil,
		&fake.IDs{},
		clock,
		cfg,
	)
	// Откат проверяется вокруг настоящего сценария, включая постановку обеих задач.
	failure := errors.New("откат после выполнения сценария")
	failed := appcheckout.NewService(
		&rollbackCheckoutUOW{UnitOfWork: uow, failure: failure},
		platform,
		nil,
		nil,
		&fake.IDs{},
		clock,
		cfg,
	)
	contact := checkout.Contact{Phone: "79251234567", Name: "Покупатель"}
	if _, err := failed.CreateOrders(ctx, buyerA, c.ID(), contact); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	counts, err := readyTenants(ctx, queue, clock.Now().Add(time.Hour))
	if err != nil || counts[appcheckout.TaskCreateOrder] != 0 || counts[appcheckout.TaskExpireCreation] != 0 {
		t.Fatal(counts, err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		basket, err := tx.Carts().ByBuyer(ctx, buyerA)
		if err != nil {
			return err
		}
		current, err := tx.Checkouts().Get(ctx, c.ID())
		if err != nil {
			return err
		}
		if len(basket.Lines()) != 1 || current.Status() != checkout.StatusConfirmed {
			t.Fatal("намерение не откатилось")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state, err := service.CreateOrders(ctx, buyerA, c.ID(), checkout.Contact{Phone: "79251234567", Name: "Покупатель"})
	if err != nil {
		t.Fatal(err)
	}
	stale := checkout.Restore(state)
	request := ports.ClaimRequest{
		Now:        clock.Now(),
		Limit:      10,
		PerTenant:  2,
		LeaseUntil: clock.Now().Add(time.Second),
		Kinds:      []string{appcheckout.TaskExpireCreation},
	}
	tasks, err := fake.TasksOf(queue.Claim(ctx, request))
	if err != nil || len(tasks) != 0 {
		t.Fatal(tasks, err)
	}
	request.Kinds = []string{appcheckout.TaskCreateOrder}
	request.TenantSlots = map[ids.TenantID]int{"t1": 0}
	tasks, err = fake.TasksOf(queue.Claim(ctx, request))
	if err != nil || len(tasks) != 0 {
		t.Fatal(tasks, err)
	}
	request.TenantSlots = nil
	tasks, err = fake.TasksOf(queue.Claim(ctx, request))
	if err != nil || len(tasks) != 1 {
		t.Fatal(tasks, err)
	}
	if err := service.HandleOrderTask(ctx, tasks[0]); err != nil {
		t.Fatal(err)
	}
	if err := queue.Complete(ctx, tasks[0].ID, tasks[0].Attempts, clock.Now()); err != nil {
		t.Fatal(err)
	}
	purchases, err := service.Purchases(ctx, buyerA, "")
	if err != nil || len(purchases.Items) != 1 {
		t.Fatal(purchases, err)
	}
	if err := service.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	counts, err = readyTenants(ctx, queue, clock.Now())
	if err != nil || counts[appcheckout.TaskPollOrder] != 0 {
		t.Fatal("до срока опрос не нужен", counts, err)
	}
	state, err = service.Get(ctx, buyerA, c.ID())
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(state.Groups[0].ExpiresAt.Add(checkout.PaymentExpiryGrace).Sub(clock.Now()))
	if err := service.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	counts, err = readyTenants(ctx, queue, clock.Now())
	if err != nil || counts[appcheckout.TaskPollOrder] != 1 {
		t.Fatal("индексируемая сверка", counts, err)
	}
	if _, err := service.Cancel(ctx, buyerA, c.ID()); err != nil {
		t.Fatal(err)
	}
	stuck, err := service.OperatorList(ctx, checkout.ListCursor{}, true)
	if err != nil || len(stuck.Items) != 1 || !stuck.Items[0].Groups[0].ReconciliationRequired {
		t.Fatal(stuck, err)
	}
	if _, err := stale.Cancel(clock.Now()); err != nil {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error { return tx.Checkouts().Update(ctx, stale) })
	if !errors.Is(err, checkout.ErrConflict) {
		t.Fatal("устаревший снимок переписал итог", err)
	}
	clock.Advance(15 * time.Second)
	request.Now, request.LeaseUntil = clock.Now(), clock.Now().Add(time.Second)
	request.Kinds = []string{appcheckout.TaskExpireCreation}
	tasks, err = fake.TasksOf(queue.Claim(ctx, request))
	if err != nil || len(tasks) != 1 {
		t.Fatal(tasks, err)
	}
	if err := service.HandleOrderTask(ctx, tasks[0]); err != nil {
		t.Fatal(err)
	}
	request.Now, request.LeaseUntil = clock.Now(), clock.Now().Add(time.Second)
	request.Kinds = []string{appcheckout.TaskCancelOrder}
	tasks, err = fake.TasksOf(queue.Claim(ctx, request))
	if err != nil || len(tasks) != 1 {
		t.Fatal("отмена поставлена вместе с переходом", tasks, err)
	}
	platform.FailCancel("t1", context.DeadlineExceeded)
	if err := service.HandleOrderTask(ctx, tasks[0]); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	state, err = service.Get(ctx, buyerA, c.ID())
	if err != nil || state.Groups[0].CancellationAttempts != 1 ||
		state.Groups[0].CancellationLastErrorCode != "platform.unavailable" {
		t.Fatal(state, err)
	}
	platform.FailCancel("t1", nil)
	if err := service.HandleOrderTask(ctx, tasks[0]); err != nil {
		t.Fatal(err)
	}
	if err := queue.Complete(ctx, tasks[0].ID, tasks[0].Attempts, clock.Now()); err != nil {
		t.Fatal(err)
	}
	state, err = service.Get(ctx, buyerA, c.ID())
	if err != nil || !state.Groups[0].CancellationConfirmed || state.Groups[0].ReconciliationRequired ||
		state.Groups[0].Status != checkout.GroupCancelled || state.Groups[0].CancellationAttempts != 2 ||
		state.Groups[0].CancellationLastErrorCode != "" {
		t.Fatal(state, err)
	}
	stuck, err = service.OperatorList(ctx, checkout.ListCursor{}, true)
	if err != nil || len(stuck.Items) != 0 {
		t.Fatal(stuck, err)
	}

}

// readyTenants — магазины с созревшей задачей по видам.
func readyTenants(ctx context.Context, queue *postgres.TaskQueue, now time.Time) (map[string]int64, error) {
	lanes, err := queue.ReadyLanes(ctx, now)
	counts := map[string]int64{}
	for _, lane := range lanes {
		counts[lane.Kind] = lane.Tenants
	}
	return counts, err
}
