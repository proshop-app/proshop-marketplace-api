//go:build integration

package postgres_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

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
		seedPickupHistoryPurchase(t, uow, seed)
	}

	var points []checkout.Selection
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		var err error
		points, err = tx.Checkouts().PickupHistory(ctx, buyerA, 5)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	want := []checkout.Selection{pickupHistoryPoint("P1"), pickupHistoryPoint("P2")}
	if !reflect.DeepEqual(points, want) {
		t.Fatalf("история пунктов = %+v, want %+v", points, want)
	}

	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		var err error
		points, err = tx.Checkouts().PickupHistory(ctx, buyerA, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if len(points) != 1 || points[0].PickupPointCode != "P1" {
		t.Fatalf("ограничение истории = %+v", points)
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

func seedPickupHistoryPurchase(t *testing.T, uow *postgres.UnitOfWork, seed pickupHistorySeed) {
	t.Helper()

	purchase, err := checkout.New(
		seed.id,
		seed.buyer,
		"pickup-history-"+string(seed.id),
		[]checkout.Line{
			{
				Offer:    ids.OfferID{Tenant: "t1", Product: "p1"},
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
		"t1": {Status: delivery.QuoteAvailable, ExpiresAt: seed.created.Add(time.Hour)},
	}
	if err := purchase.SelectDelivery(pickupHistoryPoint(seed.point), quotes, seed.created); err != nil {
		t.Fatal(err)
	}

	if seed.ordered {
		attachPickupHistoryOrder(t, purchase, seed)
	}

	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, purchase)
	}); err != nil {
		t.Fatal(err)
	}
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
