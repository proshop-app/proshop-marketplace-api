//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const breakPickupHistoryDelivery = `UPDATE checkouts SET delivery = '{"city":123}' WHERE id = $1`

type pickupHistorySeed struct {
	id      ids.UUID
	buyer   ids.UUID
	point   string
	created time.Time
	ordered bool
}

// Подсказка берёт пункт последней покупки с заказом: повтор пункта поднимает его, черновик без заказа не виден.
func TestPickupHistoryReturnsDistinctOrderedPointsNewestFirst(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedBuyer(t, uow, buyerA, "79251234567")
	seedBuyer(t, uow, buyerB, "79251234568")

	for _, seed := range []pickupHistorySeed{
		{id: "a0000000-0000-0000-0000-000000000000", buyer: buyerA, point: "P1", created: at(0), ordered: true},
		{id: "a1000000-0000-0000-0000-000000000000", buyer: buyerA, point: "P2", created: at(10), ordered: true},
		{id: "a2000000-0000-0000-0000-000000000000", buyer: buyerA, point: "P1", created: at(20), ordered: true},
		{id: "a3000000-0000-0000-0000-000000000000", buyer: buyerA, point: "P3", created: at(30)},
		{id: "b0000000-0000-0000-0000-000000000000", buyer: buyerB, point: "P4", created: at(40), ordered: true},
	} {
		seedPickupHistoryPurchase(t, uow, "t1", seed)
	}

	var page checkout.PickupHistoryPage
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		var err error
		page, err = tx.Checkouts().PickupHistory(ctx, buyerA, 5)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	want := []checkout.Selection{pickupHistoryPoint("P1"), pickupHistoryPoint("P2")}
	if !reflect.DeepEqual(page.Points, want) || len(page.Failures) != 0 {
		t.Fatalf("история пунктов = %+v, want %+v", page, want)
	}

	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		var err error
		page, err = tx.Checkouts().PickupHistory(ctx, buyerA, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if len(page.Points) != 1 || page.Points[0].PickupPointCode != "P1" {
		t.Fatalf("ограничение истории = %+v", page)
	}
}

func TestPickupHistorySkipsInvalidDeliveryAndLogsPurchasePostgres(t *testing.T) {
	const invalid ids.UUID = "a5000000-0000-0000-0000-000000000000"
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedBuyer(t, uow, buyerA, "79251234567")
	for _, seed := range []pickupHistorySeed{
		{id: "a0000000-0000-0000-0000-000000000000", buyer: buyerA, point: "P1", created: at(0), ordered: true},
		{id: "a1000000-0000-0000-0000-000000000000", buyer: buyerA, point: "P2", created: at(10), ordered: true},
		{id: "a2000000-0000-0000-0000-000000000000", buyer: buyerA, point: "P3", created: at(20), ordered: true},
		{id: "a3000000-0000-0000-0000-000000000000", buyer: buyerA, point: "P4", created: at(30), ordered: true},
		{id: "a4000000-0000-0000-0000-000000000000", buyer: buyerA, point: "P5", created: at(40), ordered: true},
		{id: invalid, buyer: buyerA, point: "P1", created: at(50), ordered: true},
		{id: "a6000000-0000-0000-0000-000000000000", buyer: buyerA, point: "P6", created: at(5), ordered: true},
	} {
		seedPickupHistoryPurchase(t, uow, "t1", seed)
	}
	if _, err := pool.Exec(ctx, breakPickupHistoryDelivery, string(invalid)); err != nil {
		t.Fatal(err)
	}

	service := appcheckout.NewService(uow, nil, nil, nil, nil, fake.NewClock(at(50)), appcheckout.Config{})
	var logs bytes.Buffer
	captureCheckoutReadLogs(service, &logs)
	points, err := service.PickupHistory(ctx, buyerA)
	want := []checkout.Selection{
		pickupHistoryPoint("P5"),
		pickupHistoryPoint("P4"),
		pickupHistoryPoint("P3"),
		pickupHistoryPoint("P2"),
	}
	if err != nil || !reflect.DeepEqual(points, want) {
		t.Fatalf("история ПВЗ: %+v, %v", points, err)
	}
	assertCheckoutReadLog(t, &logs, invalid, "t1", "доставка в пункт P1")
}

func TestPickupHistorySnapshotsAtSamePointCanBeInvalidatedConcurrently(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedBuyer(t, uow, buyerA, "79251234567")
	seeds := []pickupHistorySeed{
		{id: "a0000000-0000-0000-0000-000000000000", buyer: buyerA, point: "P1", created: at(20), ordered: true},
		{id: "a1000000-0000-0000-0000-000000000000", buyer: buyerA, point: "P1", created: at(10), ordered: true},
	}
	for _, seed := range seeds {
		seedPickupHistoryPurchase(t, uow, "t1", seed)
	}

	first, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback(ctx) }()
	if _, err := first.Exec(ctx, breakPickupHistoryDelivery, string(seeds[0].id)); err != nil {
		t.Fatal(err)
	}
	// Вторая транзакция сохраняет изменение, пока первая держит свою строку.
	if _, err := pool.Exec(ctx, breakPickupHistoryDelivery, string(seeds[1].id)); err != nil {
		t.Fatal(err)
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	service := appcheckout.NewService(uow, nil, nil, nil, nil, fake.NewClock(at(50)), appcheckout.Config{})
	points, err := service.PickupHistory(ctx, buyerA)
	if err != nil || len(points) != 0 {
		t.Fatalf("история после повреждения обоих снимков: %+v, %v", points, err)
	}
}

func TestFirstPickupOrdersAtDifferentPointsCommitIndependently(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedBuyer(t, uow, buyerA, "79251234567")
	var purchases []*checkout.Checkout
	for i := range 5 {
		seed := pickupHistorySeed{
			id:      ids.UUID(fmt.Sprintf("a0000000-0000-4000-8000-%012d", i)),
			buyer:   buyerA,
			point:   fmt.Sprintf("P%d", i),
			created: at(i),
		}
		purchase := seedPickupHistoryPurchase(t, uow, "t1", seed)
		attachPickupHistoryOrder(t, purchase, seed)
		purchases = append(purchases, purchase)
	}

	// Все остальные транзакции должны зафиксироваться до завершения первой.
	err := do(t, uow, func(ctx context.Context, first ports.Tx) error {
		if err := first.Checkouts().Update(ctx, purchases[0]); err != nil {
			return err
		}
		writes, ctx := errgroup.WithContext(ctx)
		for _, purchase := range purchases[1:] {
			writes.Go(func() error {
				return uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
					return tx.Checkouts().Update(ctx, purchase)
				})
			})
		}
		return writes.Wait()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		page, err := tx.Checkouts().PickupHistory(ctx, buyerA, len(purchases))
		if err != nil {
			return err
		}
		if len(page.Points) != len(purchases) || len(page.Failures) != 0 {
			t.Fatalf("история после записи разных ПВЗ: %+v", page)
		}
		for i, point := range page.Points {
			if point.PickupPointCode != fmt.Sprintf("P%d", len(purchases)-1-i) {
				t.Fatalf("неожиданный ПВЗ на позиции %d: %+v", i, point)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func pickupHistoryPoint(code string) checkout.Selection {
	return checkout.Selection{
		Carrier:         "cdek",
		PickupPointCode: code,
		City:            "Москва",
		Address:         "Адрес " + code,
		RegionCode:      "RU-MOW",
	}
}

func seedPickupHistoryPurchase(
	t *testing.T,
	uow *postgres.UnitOfWork,
	tenant ids.TenantID,
	seed pickupHistorySeed,
) *checkout.Checkout {
	t.Helper()

	purchase, err := checkout.New(
		seed.id,
		seed.buyer,
		"pickup-history-"+string(seed.id),
		[]checkout.Line{
			{
				Offer:    ids.OfferID{Tenant: tenant, Product: "p1"},
				Name:     "Товар",
				Quantity: 1,
				Price:    money.FromKopecks(100),
			},
		},
		seed.created,
	)
	if err != nil {
		t.Fatal(err)
	}

	quotes := map[ids.TenantID]delivery.Quote{
		tenant: {Status: delivery.QuoteAvailable, ExpiresAt: seed.created.Add(time.Hour)},
	}
	if err := purchase.SelectDelivery(pickupHistoryPoint(seed.point), quotes, seed.created); err != nil {
		t.Fatal(err)
	}

	if seed.ordered {
		attachPickupHistoryOrder(t, purchase, seed)
		if _, err := purchase.MarkCartConsumed(seed.created); err != nil {
			t.Fatal(err)
		}
	}

	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, purchase)
	}); err != nil {
		t.Fatal(err)
	}
	return purchase
}

func attachPickupHistoryOrder(t *testing.T, purchase *checkout.Checkout, seed pickupHistorySeed) {
	t.Helper()

	if err := purchase.Confirm(seed.created); err != nil {
		t.Fatal(err)
	}

	contact := checkout.Contact{Phone: "79251234567", Name: "Покупатель"}
	if _, err := purchase.BeginOrders(contact, seed.created); err != nil {
		t.Fatal(err)
	}

	for _, group := range purchase.Groups() {
		total, err := group.Goods.Add(group.DeliveryPrice)
		if err != nil {
			t.Fatal(err)
		}

		order := checkout.SellerOrder{
			Carrier:         "cdek",
			PickupPointCode: seed.point,
			OrderID:         "o-" + string(seed.id),
			ExpiresAt:       group.ExpiresAt,
			Total:           total,
		}
		if _, err := purchase.AttachOrder(group.TenantID, order, seed.created); err != nil {
			t.Fatal(err)
		}
	}
}
