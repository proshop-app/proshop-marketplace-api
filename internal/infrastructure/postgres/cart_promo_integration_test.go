//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestCartPromoPersistenceAndConflict(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	seedBuyer(t, uow, buyerA, "79251234567")
	basket := cart.New(buyerB, buyerA, at(0), time.Hour)
	offer := cart.Offer{
		ID:        ids.OfferID{Tenant: "t1", Product: "p1"},
		Name:      "Товар",
		Price:     money.FromKopecks(100),
		Available: 10,
		Visible:   true,
	}
	if err := basket.Add(offer, 3, at(0), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := basket.SetPromo("t1", "SAVE", at(0), time.Hour); err != nil {
		t.Fatal(err)
	}
	discounts := map[ids.ProductID][]cart.Discount{"p1": {{PromotionID: "promo1", Amount: money.FromKopecks(11)}}}
	basket.ApplyPromo(
		"t1",
		cart.PromoCode{Code: "SAVE", Valid: true, Discount: money.FromKopecks(11)},
		discounts,
		at(0),
		time.Hour,
	)
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Carts().Save(ctx, basket)
	}); err != nil {
		t.Fatal(err)
	}
	var loaded *cart.Cart
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		var err error
		loaded, err = tx.Carts().ByBuyer(ctx, buyerA)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	total, err := loaded.Total()
	if err != nil || total.Kopecks() != 289 || loaded.State().Promos["t1"].Code != "SAVE" {
		t.Fatal(loaded.State(), err)
	}
	stale := cart.Restore(loaded.State())
	_, _ = loaded.SetPromo("t1", "", at(1), time.Hour)
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Carts().Save(ctx, loaded)
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = stale.SetPromo("t1", "OLD", at(1), time.Hour)
	err = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Carts().Save(ctx, stale)
	})
	if !errors.Is(err, cart.ErrConflict) {
		t.Fatal(err)
	}
}

func TestCheckoutPromoSnapshotPersistence(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	seedBuyer(t, uow, buyerA, "79251234567")
	purchase, err := checkout.New(
		buyerB,
		buyerA,
		"promo-checkout-key",
		[]checkout.Line{
			{
				Offer:     ids.OfferID{Tenant: "t1", Product: "p1"},
				Name:      "Товар",
				Quantity:  3,
				Price:     money.FromKopecks(100),
				Discounts: []cart.Discount{{PromotionID: "promo1", Amount: money.FromKopecks(11)}},
			},
		},
		at(0),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := purchase.SetPromoCodes(map[ids.TenantID]string{"t1": "SAVE"}, at(0)); err != nil {
		t.Fatal(err)
	}
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, purchase)
	}); err != nil {
		t.Fatal(err)
	}
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		loaded, err := tx.Checkouts().Get(ctx, buyerB)
		if err != nil {
			return err
		}
		group := loaded.Groups()[0]
		if group.PromoCode != "SAVE" || group.Goods.Kopecks() != 289 || len(group.Lines[0].Discounts) != 1 {
			return fmt.Errorf("неверная группа: %+v", group)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
