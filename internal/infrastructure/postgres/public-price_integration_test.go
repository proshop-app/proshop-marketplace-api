//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestPublicPriceRoundTrip(t *testing.T) {
	uow := postgres.NewUnitOfWork(pgtest.Pool(t))
	for _, test := range []struct {
		name    string
		present bool
		price   int64
		base    int64
	}{
		{name: "missing"},
		{name: "zero", present: true, base: 100},
		{name: "discount", present: true, price: 80, base: 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC()
			snapshot := catalog.Snapshot{
				TenantID:  "t1",
				ProductID: "p1",
				Revision:  1,
				At:        now,
				Card:      catalog.Card{Name: "Товар", Visible: true},
				HasPrice:  test.present,
				Price:     money.FromKopecks(test.price),
				BasePrice: money.FromKopecks(test.base),
			}
			product, err := catalog.NewProduct(snapshot, now)
			if err != nil {
				t.Fatal(err)
			}
			// Повторная запись проверяет условное обновление цены агрегата.
			err = uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
				previous, err := tx.Products().Get(ctx, "t1", "p1")
				if err != nil && !errors.Is(err, catalog.ErrProductNotFound) {
					return err
				}
				if err == nil {
					snapshot := catalog.Snapshot{
						TenantID:  "t1",
						ProductID: "p1",
						Revision:  previous.State().Revision + 1,
						At:        now,
						Card:      product.State().Card,
						HasPrice:  test.present,
						Price:     product.State().Price,
						BasePrice: product.State().BasePrice,
					}
					if _, err := previous.Apply(snapshot, now); err != nil {
						return err
					}
					product = previous
				}
				return tx.Products().Save(ctx, product)
			})
			if err != nil {
				t.Fatal(err)
			}
			err = uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
				restored, err := tx.Products().Get(ctx, "t1", "p1")
				if err != nil {
					return err
				}
				state := restored.State()
				if state.HasPrice != test.present || state.Price.Kopecks() != test.price ||
					state.BasePrice.Kopecks() != test.base {
					t.Fatal(state)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
