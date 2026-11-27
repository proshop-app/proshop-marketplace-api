//go:build integration

package postgres_test

import (
	"context"
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
	lowerWatchPrices = `UPDATE catalog_products SET price = $1 WHERE tenant_id = 't1' AND product_id = 'p1'`
	hideWatchSeller  = `UPDATE participants
 SET enabled = false,
 stage = 'disconnected'
 WHERE tenant_id = 't1'`
	rejectWatchOffer = `UPDATE moderation_verdicts SET status = 'rejected'
 WHERE tenant_id = 't1' AND product_id = 'p1'`
	deleteWatchOffer = `UPDATE catalog_products SET deleted = true,
 card = NULL,
 price = NULL,
 base_price = NULL
 WHERE tenant_id = 't1' AND product_id = 'p1'`
	unpublishWatchOffer = `UPDATE catalog_products SET card = jsonb_set(card, '{publication,published}', 'false')
 WHERE tenant_id = 't1' AND product_id = 'p1'`
	clearWatchPrices = `UPDATE catalog_products
 SET price = NULL,
 base_price = NULL
 WHERE tenant_id = 't1' AND product_id = 'p1'`
)

type buyerListLockProbe struct {
	ports.UnitOfWork
	released chan<- struct{}
	retry    <-chan struct{}
	signaled bool
}

func seedBuyerFeatureOffers(t *testing.T, uow *postgres.UnitOfWork, products ...ids.ProductID) {
	t.Helper()
	seedBuyer(t, uow, buyerA, "79990000001")
	seedStoredCard(t, uow)
	if len(products) == 0 {
		products = []ids.ProductID{"p1", "p2"}
	}
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		shop, err := tx.Participants().Get(ctx, "t1")
		if err != nil {
			return err
		}
		if _, err := shop.ApplyShopSettings(
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
		for _, id := range products {
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
			// Две реплики должны вернуть соединения, пока блокировка списка ещё занята.
			locked, release, holding := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				holding <- uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
					if err := tx.BuyerFeatures().TryLockLists(ctx, buyerA); err != nil {
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
			results, released := make(chan error, 2), make(chan struct{}, 2)
			retry := make(chan struct{})
			for _, id := range []ids.ProductID{"p1", "p2"} {
				probe := &buyerListLockProbe{
					UnitOfWork: postgres.NewUnitOfWork(pool),
					released:   released,
					retry:      retry,
				}
				service := appbuyer.NewFeatures(probe, nil, nil)
				go func() {
					results <- service.PutSaved(ctx, buyerA, kind, ids.OfferID{Tenant: "t1", Product: id}, nil)
				}()
			}
			for range 2 {
				select {
				case <-released:
				case err := <-results:
					t.Fatalf("добавление завершилось до освобождения списка: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if got := pool.Stat().AcquiredConns(); got != 1 {
				t.Fatalf("занято %d соединений, ожидается только владелец блокировки", got)
			}
			close(release)
			if err := <-holding; err != nil {
				t.Fatal(err)
			}
			close(retry)
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
				inserted := 0
				for _, id := range []ids.ProductID{"p1", "p2"} {
					offer := ids.OfferID{Tenant: "t1", Product: id}
					count, exists, err := tx.BuyerFeatures().SavedStats(ctx, buyerA, kind, offer)
					if err != nil {
						return err
					}
					if count != limit {
						return fmt.Errorf("число записей: %d вместо %d", count, limit)
					}
					if exists {
						inserted++
					}
				}
				if inserted != 1 {
					return fmt.Errorf("добавлено %d из двух новых предложений", inserted)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBuyerCollectionConcurrentWrites(t *testing.T) {
	for _, kind := range []ports.SavedKind{ports.Favorites, ports.PriceWatches} {
		t.Run(string(kind), func(t *testing.T) {
			const count = 96
			pool := pgtest.Pool(t)
			products := make([]ids.ProductID, count)
			for i := range products {
				products[i] = ids.ProductID(fmt.Sprintf("p%d", i+1))
			}
			seedBuyerFeatureOffers(t, postgres.NewUnitOfWork(pool), products...)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			replicas := make([]*appbuyer.Features, 3)
			for i := range replicas {
				replicas[i] = appbuyer.NewFeatures(postgres.NewUnitOfWork(pool), nil, nil)
			}
			start, results := make(chan struct{}), make(chan error, count)
			for i, product := range products {
				go func() {
					<-start
					offer := ids.OfferID{Tenant: "t1", Product: product}
					results <- replicas[i%len(replicas)].PutSaved(ctx, buyerA, kind, offer, nil)
				}()
			}
			close(start)
			for range products {
				if err := <-results; err != nil {
					t.Error(err)
				}
			}
			err := postgres.NewUnitOfWork(pool).Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				offer := ids.OfferID{Tenant: "t1", Product: products[0]}
				got, exists, err := tx.BuyerFeatures().SavedStats(ctx, buyerA, kind, offer)
				if err == nil && (got != count || !exists) {
					return fmt.Errorf("число записей: %d, первая запись найдена: %t", got, exists)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func (p *buyerListLockProbe) Do(ctx context.Context, run func(context.Context, ports.Tx) error) error {
	err := p.UnitOfWork.Do(ctx, run)
	if p.signaled || !errors.Is(err, buyer.ErrFeaturesBusy) {
		return err
	}
	p.signaled = true
	select {
	case p.released <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-p.retry:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestBuyerPriceDropSQLAndFavoritePage(t *testing.T) {
	uow := postgres.NewUnitOfWork(pgtest.Pool(t))
	seedBuyerFeatureOffers(t, uow)
	ctx := context.Background()
	service := appbuyer.NewFeatures(uow, nil, &fake.PickupLookup{})
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
	if err != nil || profile.Alerts.PriceDrops != 0 {
		t.Fatal(profile, err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		product, err := tx.Products().Get(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		snap := snapshot("t1", "p1", 2, 1)
		snap.Card.Publication = taxonomy.Publication{Published: true}
		snap.Price = money.FromKopecks(9000)
		if _, err := product.Apply(snap, at(1)); err != nil {
			return err
		}
		return tx.Products().Save(ctx, product)
	}); err != nil {
		t.Fatal(err)
	}
	profile, err = service.Profile(ctx, buyerA)
	if err != nil || profile.Alerts.PriceDrops != 1 {
		t.Fatal(profile, err)
	}
	target = money.FromKopecks(8000)
	if err := service.PutSaved(ctx, buyerA, ports.PriceWatches, offer, &target); err != nil {
		t.Fatal(err)
	}
	profile, err = service.Profile(ctx, buyerA)
	if err != nil || profile.Alerts.PriceDrops != 0 {
		t.Fatal(profile, err)
	}
}

func TestPriceDropCounterMatchesList(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
		drops int
	}{
		{name: "публичный", drops: 1},
		{name: "скрытый участник", query: hideWatchSeller},
		{name: "отклонённый вердикт", query: rejectWatchOffer},
		{name: "удалённый товар", query: deleteWatchOffer},
		{name: "неопубликованная карточка", query: unpublishWatchOffer},
		{name: "нет публичной цены", query: clearWatchPrices},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := pgtest.Pool(t)
			uow := postgres.NewUnitOfWork(pool)
			seedBuyerFeatureOffers(t, uow)
			ctx := context.Background()
			service := appbuyer.NewFeatures(uow, nil, &fake.PickupLookup{})
			offer := ids.OfferID{Tenant: "t1", Product: "p1"}
			if err := service.PutSaved(ctx, buyerA, ports.PriceWatches, offer, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, lowerWatchPrices, int64(9000)); err != nil {
				t.Fatal(err)
			}
			if test.query != "" {
				if _, err := pool.Exec(ctx, test.query); err != nil {
					t.Fatal(err)
				}
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
			if err != nil || profile.Alerts.PriceDrops != count || count != test.drops {
				t.Fatal(profile, count, err)
			}
			if list[0].Offer.UpdatedAt.IsZero() {
				t.Fatal("нулевая дата снимка")
			}
		})
	}
}
