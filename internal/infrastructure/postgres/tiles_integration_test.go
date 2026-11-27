//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestTileSnapshotsTrackMovesAndMemberVersions(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedTenant(t, uow, "t1", "shop-one", participant.TenantActive)
	ctx := context.Background()
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		seller, err := participant.New(tenant, "Магазин", at(0))
		if err != nil {
			return err
		}
		return tx.Participants().Add(ctx, seller)
	}); err != nil {
		t.Fatal(err)
	}

	save := func(id ids.ProductID, revision int64, key string) {
		t.Helper()
		err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			source := snapshot("t1", id, revision, int(revision))
			source.Card.TileKey = key
			var product *catalog.Product
			var err error
			if revision == 1 {
				product, err = catalog.NewProduct(source, source.At)
			} else {
				product, err = tx.Products().Get(ctx, "t1", id)
				if err == nil {
					_, err = product.Apply(source, source.At)
				}
			}
			if err != nil {
				return err
			}
			return tx.Products().Save(ctx, product)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	save("p1", 1, "color-a")
	save("p2", 1, "color-a")
	read := func() []ports.TileState {
		t.Helper()
		var tiles []ports.TileState
		err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			var err error
			offers := []ids.OfferID{{Tenant: "t1", Product: "p1"}}
			former := []ports.TileKey{{TenantID: "t1", Key: "color-a"}}
			tiles, err = tx.Offers().Tiles(ctx, offers, former)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return tiles
	}
	initial := read()
	if len(initial) != 1 || len(initial[0].Members) != 2 ||
		!initial[0].Members[0].Changed || initial[0].Members[1].Changed {
		t.Fatalf("согласованный состав плитки: %+v", initial)
	}
	save("p1", 2, "color-b")
	moved := read()
	if len(moved) != 2 || len(moved[0].Members) != 1 || moved[0].Version <= initial[0].Version {
		t.Fatalf("перенос: %+v", moved)
	}
	save("p2", 2, "color-b")
	empty := read()
	if len(empty) != 2 || len(empty[0].Members) != 0 || empty[0].Version <= moved[0].Version ||
		len(empty[1].Members) != 2 || empty[1].Version <= moved[1].Version {
		t.Fatalf("пустая плитка: %+v", empty)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		pages := []ports.TilePage{
			{Limit: 1},
			{TenantID: "t1", Limit: 1},
			{Since: time.Now().Add(-time.Minute), Limit: 1},
		}
		for i, page := range pages {
			got, err := tx.Offers().TilePage(ctx, page)
			if err != nil || len(got) != 1 {
				return fmt.Errorf("страница %d: %d, %w", i, len(got), err)
			}
			next := page
			next.After = got[0].Key
			got, err = tx.Offers().TilePage(ctx, next)
			if err != nil || i < 2 && len(got) != 0 || i == 2 && (len(got) != 0 && got[0].Key.Key != "color-b") {
				return fmt.Errorf("следующая страница %d: %d, %w", i, len(got), err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Снятие магазина обходит также прежние ключи, у которых уже нет товара.
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		purge := tx.TenantPurge()
		if _, err := purge.Begin(ctx, "t1"); err != nil {
			return err
		}
		products, ok, err := purge.IndexPage(ctx, "t1", 0, 10)
		want := []ids.ProductID{ports.TileDocumentPrefix + "color-a", ports.TileDocumentPrefix + "color-b"}
		if err != nil || !ok || !slices.Equal(products, want) {
			return fmt.Errorf("удаление плиток: %v, %w", products, err)
		}
		cursor := ports.TenantPurgeCursor{Step: 1, Product: want[0]}
		if err := purge.Advance(ctx, "t1", cursor); err != nil {
			return err
		}
		products, ok, err = purge.IndexPage(ctx, "t1", 1, 10)
		if err != nil || !ok || !slices.Equal(products, want[1:]) {
			return fmt.Errorf("следующая страница плиток: %v, %w", products, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
