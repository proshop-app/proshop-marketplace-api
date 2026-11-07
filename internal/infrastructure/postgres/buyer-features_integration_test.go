//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestBuyerFeaturesRepositories(t *testing.T) {
	uow := postgres.NewUnitOfWork(pgtest.Pool(t))
	seedBuyer(t, uow, buyerA, "79990000001")
	ctx := context.Background()
	var stale *buyer.Profile
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		repo := tx.BuyerFeatures()
		p, err := repo.Profile(ctx, buyerA)
		if err != nil {
			return err
		}
		stale = buyer.RestoreProfile(p.State())
		if _, err := p.Change(buyer.ProfileState{Name: "Имя"}); err != nil {
			return err
		}
		if err := repo.SaveProfile(ctx, buyerA, p); err != nil {
			return err
		}
		target := money.FromKopecks(80)
		item := ports.SavedOffer{
			Offer:        ports.OfferSnapshot{ID: "t1~p1", Name: "Товар"},
			PriceAtWatch: money.FromKopecks(100),
			TargetPrice:  &target,
		}
		for _, kind := range []ports.SavedKind{ports.Favorites, ports.PriceWatches} {
			if err := repo.PutSaved(ctx, buyerA, kind, item); err != nil {
				return err
			}
			item.PriceAtWatch = money.FromKopecks(90)
			if err := repo.PutSaved(ctx, buyerA, kind, item); err != nil {
				return err
			}
			item.PriceAtWatch = money.FromKopecks(100)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = stale.Change(buyer.ProfileState{Name: "Старый"})
	if err != nil {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.BuyerFeatures().SaveProfile(ctx, buyerA, stale)
	})
	if !errors.Is(err, buyer.ErrProfileConflict) {
		t.Fatalf("конфликт вставки: %v", err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		repo := tx.BuyerFeatures()
		p, err := repo.Profile(ctx, buyerA)
		if err != nil {
			return err
		}
		if p.State().Name != "Имя" || p.State().Version != 1 {
			return fmt.Errorf("профиль: %+v", p.State())
		}
		stale = buyer.RestoreProfile(p.State())
		state := p.State()
		state.Name = "Новое"
		if _, err := p.Change(state); err != nil {
			return err
		}
		if err := repo.SaveProfile(ctx, buyerA, p); err != nil {
			return err
		}
		for _, kind := range []ports.SavedKind{ports.Favorites, ports.PriceWatches} {
			var items []ports.SavedOffer
			var err error
			if kind == ports.Favorites {
				items, _, err = repo.FavoritesPage(ctx, buyerA, buyer.FavoritesLimit, 0)
			} else {
				items, err = repo.Saved(ctx, buyerA)
			}
			if err != nil {
				return err
			}
			if len(items) != 1 {
				return fmt.Errorf("снимок: %+v", items)
			}
			if kind == ports.PriceWatches &&
				(items[0].PriceAtWatch.Kopecks() != 100 || items[0].TargetPrice.Kopecks() != 80) {
				return fmt.Errorf("снимок: %+v", items)
			}
			var other []ports.SavedOffer
			if kind == ports.Favorites {
				other, _, err = repo.FavoritesPage(ctx, buyerB, buyer.FavoritesLimit, 0)
			} else {
				other, err = repo.Saved(ctx, buyerB)
			}
			if err != nil {
				return err
			}
			if len(other) != 0 {
				return errors.New("чужие записи")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	state := stale.State()
	state.Name = "Устаревшее"
	if _, err := stale.Change(state); err != nil {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.BuyerFeatures().SaveProfile(ctx, buyerA, stale)
	})
	if !errors.Is(err, buyer.ErrProfileConflict) {
		t.Fatalf("условный UPDATE: %v", err)
	}
	failure := errors.New("откат")
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.BuyerFeatures().DeleteSaved(ctx, buyerA, ports.Favorites, "t1~p1"); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		items, _, err := tx.BuyerFeatures().FavoritesPage(ctx, buyerA, buyer.FavoritesLimit, 0)
		if err != nil {
			return err
		}
		if len(items) != 1 {
			return errors.New("удаление не откатилось")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestShopSubscriptionAggregate(t *testing.T) {
	uow := postgres.NewUnitOfWork(pgtest.Pool(t))
	seedBuyer(t, uow, buyerA, "79990000001")
	seedBuyer(t, uow, buyerB, "79990000002")
	seedTenant(t, uow, "t1", "shop", participant.TenantActive)
	err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		shop, err := participant.New(tenant, "Магазин", at(0))
		if err != nil {
			return err
		}
		if err := tx.Participants().Add(ctx, shop); err != nil {
			return err
		}
		repo := tx.BuyerFeatures()
		for _, id := range []ids.UUID{buyerA, buyerB, buyerA} {
			if err := repo.Lock(ctx, id); err != nil {
				return err
			}
			if err := repo.Subscribe(ctx, id, "t1", ports.Subscribed); err != nil {
				return err
			}
		}
		count, subscribed, err := repo.Subscribers(ctx, "t1", buyerA)
		if err != nil {
			return err
		}
		if count != 2 || !subscribed {
			return fmt.Errorf("подписчики: %d %v", count, subscribed)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		repo := tx.BuyerFeatures()
		for i := 0; i < 2; i++ {
			if err := repo.Subscribe(ctx, buyerA, "t1", ports.Unsubscribed); err != nil {
				return err
			}
		}
		count, subscribed, err := repo.Subscribers(ctx, "t1", buyerA)
		if err != nil {
			return err
		}
		if count != 1 || subscribed {
			return fmt.Errorf("отписка: %d %v", count, subscribed)
		}
		count, subscribed, err = repo.Subscribers(ctx, "t1", "")
		if err != nil {
			return err
		}
		if count != 1 || subscribed {
			return fmt.Errorf("аноним: %d %v", count, subscribed)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBuyerBatchReadsProjection(t *testing.T) {
	uow := postgres.NewUnitOfWork(pgtest.Pool(t))
	err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		for _, id := range []ids.ProductID{"p1", "p2"} {
			product, err := catalog.NewProduct(snapshot("t1", id, 1, 0), at(0))
			if err != nil {
				return err
			}
			if err := tx.Products().Save(ctx, product); err != nil {
				return err
			}
		}
		states, err := tx.Offers().Batch(ctx, []ids.OfferID{
			{Tenant: "t1", Product: "p1"},
			{Tenant: "t1", Product: "p2"},
			{Tenant: "t1", Product: "absent"},
		})
		if err != nil {
			return err
		}
		if len(states) != 2 {
			return fmt.Errorf("пакет проекции: %d", len(states))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionsIncludePublicPageWithoutReadyCard(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testPool(t))
	seedRecommendationTenant(t, uow, "t1")
	seedBuyer(t, uow, buyerA, "79990000001")
	record := ports.StoreRecord{
		PagePublic: true,
		Public:     false,
		Header:     ports.ShopHeader{TenantID: "t1", Slug: "shop"},
		Card:       ports.ShopCard{TenantID: "t1", Slug: "shop"},
	}
	save := func() {
		t.Helper()
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			return tx.StoreCards().Save(ctx, record)
		}); err != nil {
			t.Fatal(err)
		}
	}
	save()
	service := appbuyer.NewFeatures(uow, nil, fake.NewPlatform(), fake.NewLimiter())
	if err := service.Subscribe(ctx, buyerA, "t1", ports.Subscribed); err != nil {
		t.Fatal(err)
	}
	shops, err := service.Subscriptions(ctx, buyerA)
	if err != nil || len(shops) != 1 || shops[0].TenantID != "t1" {
		t.Fatal(shops, err)
	}
	record.PagePublic = false
	save()
	shops, err = service.Subscriptions(ctx, buyerA)
	if err != nil || len(shops) != 0 {
		t.Fatal(shops, err)
	}
	if err := service.Subscribe(ctx, buyerB, "t1", ports.Subscribed); !errors.Is(err, participant.ErrNotFound) {
		t.Fatal(err)
	}
}
