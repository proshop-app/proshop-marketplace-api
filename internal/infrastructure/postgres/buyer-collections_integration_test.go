//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	waitingBuyerLocks = `SELECT count(*) FROM pg_locks
 WHERE locktype = 'advisory' AND NOT granted
 AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`
	lowerWatchPrices = `UPDATE catalog_products SET price_profiles = $1 WHERE tenant_id = 't1' AND product_id = 'p1'`
	hideWatchSeller  = `UPDATE participants
 SET enabled = false,
 stage = 'disconnected'
 WHERE tenant_id = 't1'`
	rejectWatchOffer = `UPDATE moderation_verdicts SET status = 'rejected'
 WHERE tenant_id = 't1' AND product_id = 'p1'`
	deleteWatchOffer = `UPDATE catalog_products SET deleted = true, card = NULL, price = NULL
 WHERE tenant_id = 't1' AND product_id = 'p1'`
	unpublishWatchOffer = `UPDATE catalog_products SET card = jsonb_set(card, '{publication,published}', 'false')
 WHERE tenant_id = 't1' AND product_id = 'p1'`
	clearWatchPrices = `UPDATE catalog_products SET price_profiles = '[]' WHERE tenant_id = 't1' AND product_id = 'p1'`
)

func seedBuyerFeatureOffers(t *testing.T, uow *postgres.UnitOfWork) {
	t.Helper()
	seedBuyer(t, uow, buyerA, "79990000001")
	seedStoredCard(t, uow)
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		shop, err := tx.Participants().Get(ctx, "t1")
		if err != nil {
			return err
		}
		if _, err := shop.ApplyShopSettings(
			w3RepositoriesIntegrationSubsidy,
			money.Money{},
			w3RepositoriesIntegrationDelivery,
			at(1),
		); err != nil {
			return err
		}
		if _, err := shop.Enable(at(1)); err != nil {
			return err
		}
		if err := tx.Participants().Update(ctx, shop); err != nil {
			return err
		}
		for _, id := range []ids.ProductID{"p1", "p2"} {
			snap := snapshot("t1", id, 1, 0)
			snap.Card.Publication = taxonomy.Publication{Published: true}
			product, err := catalog.NewProduct(snap, at(0))
			if err != nil {
				return err
			}
			if err := tx.Products().Save(ctx, product); err != nil {
				return err
			}
			verdict := moderation.NewVerdict("t1", id)
			verdict.Evaluate(1, 1, moderation.Rules{Revision: 1}, moderation.Input{}, at(0))
			if err := tx.ModerationVerdicts().Save(ctx, verdict); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBuyerCollectionConcurrentLimit(t *testing.T) {
	for _, kind := range []ports.SavedKind{ports.Favorites, ports.PriceWatches} {
		t.Run(string(kind), func(t *testing.T) {
			pool := pgtest.Pool(t)
			uow := postgres.NewUnitOfWork(pool)
			seedBuyerFeatureOffers(t, uow)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			limit := buyer.FavoritesLimit
			if kind == ports.PriceWatches {
				limit = buyer.PriceWatchesLimit
			}
			if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				for i := 0; i < limit-1; i++ {
					item := ports.SavedOffer{Offer: ports.OfferSnapshot{ID: fmt.Sprintf("t1~saved-%d", i)}}
					if err := tx.BuyerFeatures().PutSaved(ctx, buyerA, kind, item); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// Удерживаем блокировку, чтобы обе транзакции начали добавление до освобождения места проверки.
			locked, release, holding := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				holding <- uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
					if err := tx.BuyerFeatures().Lock(ctx, buyerA); err != nil {
						return err
					}
					close(locked)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
			select {
			case <-locked:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			service := appbuyer.NewFeatures(uow, nil, fake.NewPlatform(), fake.NewLimiter())
			results, started := make(chan error, 2), make(chan struct{}, 2)
			for _, id := range []ids.ProductID{"p1", "p2"} {
				go func() {
					started <- struct{}{}
					results <- service.PutSaved(ctx, buyerA, kind, ids.OfferID{Tenant: "t1", Product: id}, nil)
				}()
			}
			<-started
			<-started
			for {
				var waiting int
				err := pool.QueryRow(ctx, waitingBuyerLocks).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting == 2 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			close(release)
			if err := <-holding; err != nil {
				t.Fatal(err)
			}
			successes, limited := 0, 0
			for i := 0; i < 2; i++ {
				err := <-results
				switch {
				case err == nil:
					successes++
				case errors.Is(err, buyer.ErrCollectionLimit):
					limited++
				default:
					t.Fatal(err)
				}
			}
			if successes != 1 || limited != 1 {
				t.Fatal(successes, limited)
			}
			if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				var saved []ports.SavedOffer
				var err error
				if kind == ports.Favorites {
					saved, _, err = tx.BuyerFeatures().FavoritesPage(ctx, buyerA, buyer.FavoritesLimit, 0)
				} else {
					saved, err = tx.BuyerFeatures().Saved(ctx, buyerA)
				}
				if err == nil && len(saved) != limit {
					return fmt.Errorf("число записей: %d", len(saved))
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBuyerPriceDropSQLAndFavoritePage(t *testing.T) {
	uow := postgres.NewUnitOfWork(pgtest.Pool(t))
	seedBuyerFeatureOffers(t, uow)
	ctx := context.Background()
	service := appbuyer.NewFeatures(uow, nil, fake.NewPlatform(), fake.NewLimiter())
	for _, id := range []ids.ProductID{"p1", "p2"} {
		offer := ids.OfferID{Tenant: "t1", Product: id}
		if err := service.PutSaved(ctx, buyerA, ports.Favorites, offer, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, offset := range []int{0, 1, 2, 100} {
		items, total, err := service.Favorites(ctx, buyerA, 1, offset)
		expected := 0
		if offset < 2 {
			expected = 1
		}
		if err != nil || total != 2 || len(items) != expected {
			t.Fatal(items, total, err)
		}
	}
	target := money.FromKopecks(12000)
	offer := ids.OfferID{Tenant: "t1", Product: "p1"}
	if err := service.PutSaved(ctx, buyerA, ports.PriceWatches, offer, &target); err != nil {
		t.Fatal(err)
	}
	profile, err := service.Profile(ctx, buyerA)
	if err != nil || (profile.Alerts.PriceDrops == nil || *profile.Alerts.PriceDrops != 0) {
		t.Fatal(profile, err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		product, err := tx.Products().Get(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		snap := snapshot("t1", "p1", 2, 1)
		snap.Card.Publication = taxonomy.Publication{Published: true}
		snap.PriceProfiles[0].EffectivePrice = money.FromKopecks(9000)
		if _, err := product.Apply(snap, at(1)); err != nil {
			return err
		}
		return tx.Products().Save(ctx, product)
	}); err != nil {
		t.Fatal(err)
	}
	profile, err = service.Profile(ctx, buyerA)
	if err != nil || (profile.Alerts.PriceDrops == nil || *profile.Alerts.PriceDrops != 1) {
		t.Fatal(profile, err)
	}
	target = money.FromKopecks(8000)
	if err := service.PutSaved(ctx, buyerA, ports.PriceWatches, offer, &target); err != nil {
		t.Fatal(err)
	}
	profile, err = service.Profile(ctx, buyerA)
	if err != nil || (profile.Alerts.PriceDrops == nil || *profile.Alerts.PriceDrops != 0) {
		t.Fatal(profile, err)
	}
}

func TestPriceDropCounterMatchesList(t *testing.T) {
	for _, test := range []struct {
		name              string
		query             string
		profile           string
		drops             int
		publicUnchanged   bool
		personalUnchanged bool
	}{
		{name: "публичный", drops: 1},
		{name: "скрытый участник", query: hideWatchSeller},
		{name: "отклонённый вердикт", query: rejectWatchOffer},
		{name: "удалённый товар", query: deleteWatchOffer},
		{name: "неопубликованная карточка", query: unpublishWatchOffer},
		{name: "нет профиля цены", query: clearWatchPrices},
		{name: "персональная цена", profile: "marketplace:personal", drops: 1},
		{name: "падает только общая", profile: "marketplace:personal", personalUnchanged: true},
		{name: "падает только персональная", profile: "marketplace:personal", publicUnchanged: true, drops: 1},
		{name: "неизвестный профиль", profile: "marketplace:absent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := pgtest.Pool(t)
			uow := postgres.NewUnitOfWork(pool)
			seedBuyerFeatureOffers(t, uow)
			ctx := context.Background()
			platform := fake.NewPlatform()
			service := appbuyer.NewFeatures(uow, nil, platform, fake.NewLimiter())
			profiles := []catalog.PriceProfile{
				{
					Key:            catalog.DefaultPriceProfile,
					Price:          money.FromKopecks(10000),
					EffectivePrice: money.FromKopecks(10000),
				},
				{
					Key:            "marketplace:personal",
					Price:          money.FromKopecks(20000),
					EffectivePrice: money.FromKopecks(18000),
				},
			}
			raw, err := json.Marshal(profiles)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, lowerWatchPrices, raw); err != nil {
				t.Fatal(err)
			}
			if test.profile == "marketplace:personal" {
				platform.SetBuyerProfile(buyerA, "t1", test.profile)
			}
			offer := ids.OfferID{Tenant: "t1", Product: "p1"}
			if err := service.PutSaved(ctx, buyerA, ports.PriceWatches, offer, nil); err != nil {
				t.Fatal(err)
			}
			profiles[0].EffectivePrice = money.FromKopecks(9000)
			profiles[1].EffectivePrice = money.FromKopecks(15000)
			if test.publicUnchanged {
				profiles[0].EffectivePrice = money.FromKopecks(10000)
			}
			if test.personalUnchanged {
				profiles[1].EffectivePrice = money.FromKopecks(18000)
			}
			raw, err = json.Marshal(profiles)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, lowerWatchPrices, raw); err != nil {
				t.Fatal(err)
			}
			if test.query != "" {
				if _, err := pool.Exec(ctx, test.query); err != nil {
					t.Fatal(err)
				}
			}
			if test.profile != "" {
				platform.SetBuyerProfile(buyerA, "t1", test.profile)
			}
			list, err := service.Saved(ctx, buyerA)
			if err != nil || len(list) != 1 {
				t.Fatal(list, err)
			}
			count := 0
			for _, item := range list {
				if item.Dropped {
					count++
				}
			}
			profile, err := service.Profile(ctx, buyerA)
			if err != nil || profile.Alerts.PriceDrops == nil ||
				*profile.Alerts.PriceDrops != count || count != test.drops {
				t.Fatal(profile, count, err)
			}
			if test.profile == "marketplace:personal" &&
				(list[0].PriceAtWatch != 18000 || *list[0].CurrentPrice != profiles[1].EffectivePrice.Kopecks()) {
				t.Fatal(list)
			}
			if list[0].Offer.UpdatedAt.IsZero() {
				t.Fatal("нулевая дата снимка")
			}
		})
	}
}
