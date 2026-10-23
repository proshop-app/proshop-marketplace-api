//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestSellerOrderSagaPersistenceAndConflict(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	seedBuyer(t, uow, buyerA, "79251234567")
	c, err := checkout.New(
		buyerB,
		buyerA,
		"seller-orders-key",
		[]checkout.Line{
			{
				Offer:    ids.OfferID{Tenant: "t1", Product: "p1"},
				Quantity: 1,
				Name:     "Товар",
				Price:    money.FromKopecks(10000),
			},
		},
		at(0),
	)
	if err != nil {
		t.Fatal(err)
	}
	selection := checkout.Selection{Carrier: "cdek", PickupPointCode: "P1", RegionCode: "RU-MOW"}
	if err := c.SelectDelivery(
		selection,
		map[ids.TenantID]delivery.Quote{
			"t1": {Status: delivery.QuoteAvailable, ExpiresAt: at(30)},
		},
		at(0),
	); err != nil {
		t.Fatal(err)
	}
	if err := c.Confirm(at(0)); err != nil {
		t.Fatal(err)
	}
	contact := checkout.Contact{Phone: "79251234567", Name: "Покупатель"}
	if _, err := c.BeginOrders(contact, "https://market.example/return", at(0)); err != nil {
		t.Fatal(err)
	}
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, c)
	}); err != nil {
		t.Fatal(err)
	}
	stale := checkout.Restore(c.State())
	order := checkout.SellerOrder{
		OrderID:    "o1",
		PaymentURL: "https://seller.example/pay/o1",
		ExpiresAt:  at(60),
		Total:      money.FromKopecks(10000),
	}
	if _, err := c.AttachOrder("t1", order, at(1)); err != nil {
		t.Fatal(err)
	}
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Update(ctx, c)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := stale.ClaimCreation("t1", "stale-attempt", at(2)); err != nil {
		t.Fatal(err)
	}
	err = do(t, uow, func(ctx context.Context, tx ports.Tx) error { return tx.Checkouts().Update(ctx, stale) })
	if !errors.Is(err, checkout.ErrConflict) {
		t.Fatalf("устаревшая версия: %v", err)
	}
	err = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		loaded, err := tx.Checkouts().Get(ctx, buyerB)
		if err != nil {
			return err
		}
		state := loaded.State()
		if state.Contact != contact || state.ReturnURL != "https://market.example/return" ||
			state.Groups[0].OrderID != "o1" || state.Groups[0].PaymentOrder != 1 ||
			state.Groups[0].PaymentURL != order.PaymentURL || !state.Groups[0].ExpiresAt.Equal(at(60)) ||
			state.Delivery.RegionCode != "RU-MOW" || state.Status != checkout.StatusAwaitingPayment {
			t.Fatalf("после загрузки: %+v", state)
		}
		items, err := tx.Checkouts().List(ctx, checkout.Filter{
			BuyerID:        buyerA,
			OrdersOnly:     true,
			AwaitingBefore: at(20),
			Limit:          100,
		})
		if err != nil || len(items) != 1 {
			t.Fatalf("фильтр ожидающих: %d %v", len(items), err)
		}
		order.Status = checkout.GroupPaid
		if _, err := loaded.Observe("t1", order, at(20).Add(10*time.Minute), at(20)); err != nil {
			return err
		}
		return tx.Checkouts().Update(ctx, loaded)
	})
	if err != nil {
		t.Fatal(err)
	}
}
