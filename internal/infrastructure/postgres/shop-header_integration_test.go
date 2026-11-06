//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
)

const removeHeaderShowcase = `UPDATE store_cards SET header = header - 'showcase' WHERE tenant_id = $1`

func TestShopHeaderReadsExistingCardShowcase(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	seedTenant(t, uow, "t1", "shop-one", participant.TenantActive)
	image := "https://example.test/image"
	showcase := []ports.ShopImage{
		{OfferID: "t1:p1", Image: &image},
		{OfferID: "t1:p2"},
		{OfferID: "t1:p3"},
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		p, err := participant.New(tenant, "Магазин", at(0))
		if err != nil {
			return err
		}
		if err := tx.Participants().Add(ctx, p); err != nil {
			return err
		}
		return tx.StoreCards().Save(ctx, ports.StoreRecord{
			Header:     ports.ShopHeader{TenantID: "t1", Slug: "shop-one", Name: "Магазин", Offers: 3},
			Card:       ports.ShopCard{TenantID: "t1", Slug: "shop-one", Showcase: showcase},
			Public:     true,
			PagePublic: true,
		})
	}); err != nil {
		t.Fatal(err)
	}
	// Старая сводка не требует пересчёта ради нового поля шапки.
	if _, err := pool.Exec(ctx, removeHeaderShowcase, "t1"); err != nil {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		header, err := tx.StoreCards().Shop(ctx, "shop-one")
		if err != nil {
			return err
		}
		page, err := tx.StoreCards().List(ctx, "", 20, 0)
		if err != nil {
			return err
		}
		if len(page.Items) != 1 || !reflect.DeepEqual(header.Showcase, showcase) ||
			!reflect.DeepEqual(header.Showcase, page.Items[0].Showcase) {
			t.Fatal(header, page)
		}
		_, err = tx.StoreCards().Shop(ctx, "unknown")
		if !errors.Is(err, participant.ErrNotFound) {
			t.Fatal(err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStorePagePublicationIndependentOfList(t *testing.T) {
	uow := postgres.NewUnitOfWork(pgtest.Pool(t))
	record := seedStoredCard(t, uow)
	ctx := context.Background()
	for _, pagePublic := range []bool{true, false, true} {
		record.Public = false
		record.PagePublic = pagePublic
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			if err := tx.StoreCards().Save(ctx, record); err != nil {
				return err
			}
			loaded, err := tx.StoreCards().Get(ctx, "t1")
			if err != nil {
				return err
			}
			if loaded.Public || loaded.PagePublic != pagePublic {
				t.Fatal(loaded)
			}
			_, err = tx.StoreCards().Shop(ctx, "shop")
			if (pagePublic && err != nil) || (!pagePublic && !errors.Is(err, participant.ErrNotFound)) {
				t.Fatal(err)
			}
			for _, category := range []string{"", "root"} {
				page, err := tx.StoreCards().List(ctx, category, 20, 0)
				if err != nil {
					return err
				}
				if page.Total != 0 || len(page.Items) != 0 {
					t.Fatal(page)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
