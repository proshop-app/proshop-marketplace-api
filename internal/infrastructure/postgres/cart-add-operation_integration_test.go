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
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const cartLineXmin = `SELECT xmin::text FROM cart_lines WHERE cart_id = $1 AND tenant_id = $2 AND product_id = $3`

func TestCartAddOperationCASAndLineDelta(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedBuyer(t, uow, buyerA, "79251234567")
	now := at(0)
	firstOffer := cart.Offer{
		ID:        ids.OfferID{Tenant: "t1", Product: "p1"},
		Name:      "Товар",
		Price:     money.FromKopecks(100),
		Available: 5,
		Visible:   true,
	}
	survivor := firstOffer
	survivor.ID.Tenant = "t2"

	basket := cart.New(buyerB, buyerA, now, time.Hour)
	for _, offer := range []cart.Offer{firstOffer, survivor} {
		if err := basket.Add(offer, 1, now, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Carts().Save(ctx, basket)
	}); err != nil {
		t.Fatal(err)
	}
	var original string
	row := pool.QueryRow(ctx, cartLineXmin, buyerB, survivor.ID.Tenant, survivor.ID.Product)
	if err := row.Scan(&original); err != nil {
		t.Fatal(err)
	}

	operation := cart.AddOperation{
		BuyerID:    buyerA,
		Key:        fmt.Sprintf("%d:%s", now.UnixMilli(), buyerB),
		Offer:      firstOffer.ID,
		Quantity:   1,
		Owner:      buyerA,
		LeaseUntil: now.Add(time.Minute),
		ExpiresAt:  now.Add(24 * time.Hour),
		Now:        now,
	}
	claim := func(want bool) error {
		return uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			replayed, err := tx.Carts().ClaimAdd(ctx, operation)
			if err == nil && replayed != want {
				return fmt.Errorf("повтор: %t", replayed)
			}
			return err
		})
	}
	if err := claim(false); err != nil {
		t.Fatal(err)
	}
	if err := claim(false); !errors.Is(err, cart.ErrAddInProgress) {
		t.Fatal(err)
	}
	stale := operation
	operation.Owner = buyerB
	operation.Now = operation.LeaseUntil
	operation.LeaseUntil = operation.Now.Add(time.Minute)
	if err := claim(false); err != nil {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Carts().CompleteAdd(ctx, stale)
	}); !errors.Is(err, cart.ErrAddInProgress) {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.Carts().CompleteAdd(ctx, operation); err != nil {
			return err
		}
		loaded, err := tx.Carts().ByBuyer(ctx, buyerA)
		if err != nil {
			return err
		}
		if err := loaded.Add(firstOffer, 1, operation.Now, time.Hour); err != nil {
			return err
		}
		return tx.Carts().Save(ctx, loaded)
	}); err != nil {
		t.Fatal(err)
	}
	if err := claim(true); err != nil {
		t.Fatal(err)
	}
	var after string
	err := pool.QueryRow(ctx, cartLineXmin, buyerB, survivor.ID.Tenant, survivor.ID.Product).Scan(&after)
	if err != nil || after != original {
		t.Fatal(after, original, err)
	}
}
