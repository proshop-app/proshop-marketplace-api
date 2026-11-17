//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestBulkIdentitiesSkipStaleSourceAndKeepOtherProducts(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	ctx := context.Background()
	seedTenant(t, uow, "t1", "t1", participant.TenantActive)
	products := []*catalog.Product{}
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for _, id := range []ids.ProductID{"p1", "p2"} {
			product, err := catalog.NewProduct(snapshot("t1", id, 1, 0), at(0))
			if err != nil {
				return err
			}
			if err := tx.Products().Save(ctx, product); err != nil {
				return err
			}
			product.SetIdentityKeys("model", "identity", "variant")
			products = append(products, product)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		product, err := tx.Products().Get(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		changed := snapshot("t1", "p1", 2, 0)
		changed.Card.Name = "Новая карточка"
		if _, err := product.Apply(changed, at(2)); err != nil {
			return err
		}
		return tx.Products().Save(ctx, product)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		saved, err := tx.Products().SaveIdentities(ctx, products)
		if err != nil || len(saved) != 1 || saved[0].Product != "p2" {
			t.Fatalf("saved=%v err=%v", saved, err)
		}
		first, err := tx.Products().Get(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		second, err := tx.Products().Get(ctx, "t1", "p2")
		if err != nil {
			return err
		}
		if first.State().Card.Name != "Новая карточка" || first.State().Card.ModelKey != "" ||
			second.State().Card.ModelKey != "model" || second.State().Card.VariantKey != "variant" {
			t.Fatal(first.State(), second.State())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
