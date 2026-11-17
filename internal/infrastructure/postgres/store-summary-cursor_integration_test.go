//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestStoreSummaryCursorKeepsSnapshotAcrossGroupChange(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	ctx := context.Background()
	seedTenant(t, uow, "t1", "t1", participant.TenantActive)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for n := 0; n < 450; n++ {
			snap := snapshot("t1", ids.ProductID(fmt.Sprintf("p%03d", n)), 1, 0)
			snap.Card.Publication.Published = true
			snap.Card.VariantsID = "g1"
			product, err := catalog.NewProduct(snap, at(0))
			if err != nil {
				return err
			}
			if err := tx.Products().Save(ctx, product); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[ids.ProductID]bool{}
	pages := 0
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Offers().WalkStoreSummaryOffers(ctx, "t1", func(states []ports.OfferState) error {
			pages++
			for _, state := range states {
				p := state.Product.State()
				if seen[p.ProductID] || p.Card.VariantsID != "g1" {
					t.Fatalf("повтор или другой снимок: %s, %s", p.ProductID, p.Card.VariantsID)
				}
				seen[p.ProductID] = true
			}
			if pages == 1 {
				return changeStoreSummaryGroup(ctx, uow)
			}
			return nil
		})
	})
	if err != nil || len(seen) != 450 || pages != 3 {
		t.Fatalf("products=%d pages=%d err=%v", len(seen), pages, err)
	}
}

func changeStoreSummaryGroup(ctx context.Context, uow *postgres.UnitOfWork) error {
	return uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		product, err := tx.Products().Get(ctx, "t1", "p000")
		if err != nil {
			return err
		}
		changed := snapshot("t1", "p000", 2, 0)
		changed.Card = product.State().Card
		changed.Card.VariantsID = "g2"
		if _, err := product.Apply(changed, at(2)); err != nil {
			return err
		}
		return tx.Products().Save(ctx, product)
	})
}
