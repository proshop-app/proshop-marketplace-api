//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	app "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/storecache"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	cacheGenerationSeed = `INSERT INTO store_cards (tenant_id,slug,name,offers,public,page_public,summary,header,card)
 VALUES ('t1','shop','Магазин',3,true,true,'{}','{"categories":[]}',
 '{"tenantId":"t1","slug":"shop","name":"Магазин","products":[]}')`
	cacheGenerationMetrics = `UPDATE store_cards SET offers = offers + 1,
 card = card || '{"offers":7,"fromPrice":500,"rating":{"value":5,"count":7},
 "products":[{"productId":"p1","offerId":"t1~p1","name":"Товар","image":null}]}'::jsonb
 WHERE tenant_id = 't1'`
	cacheGenerationSignature = `UPDATE store_cards SET cache_signature = $1 WHERE tenant_id = 't1'`
	cacheGenerationLogo      = `UPDATE store_cards SET card =
 jsonb_set(card,'{logo}','"new-logo"') WHERE tenant_id = 't1'`
	cacheGenerationHide     = `UPDATE store_cards SET public = false, page_public = false WHERE tenant_id = 't1'`
	cacheGenerationDelete   = `DELETE FROM store_cards WHERE tenant_id = 't1'`
	pendingStoreCardChanges = `SELECT count(*) FROM store_summary_changes WHERE tenant_id = 't1'`
)

func TestStoreCardCacheGenerationCommittedPublicChanges(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	seedTenant(t, uow, "t1", "shop", participant.TenantActive)
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		member, err := participant.New(tenant, "Магазин", at(0))
		if err != nil {
			return err
		}
		return tx.Participants().Add(ctx, member)
	}); err != nil {
		t.Fatal(err)
	}
	version := func() int64 {
		t.Helper()
		var value int64
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			var err error
			value, err = tx.StoreCards().CacheVersion(ctx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return value
	}
	initial := version()
	if _, err := pool.Exec(ctx, cacheGenerationSeed); err != nil {
		t.Fatal(err)
	}
	if version() != initial+1 {
		t.Fatal("новый публичный магазин не сменил поколение")
	}
	if _, err := pool.Exec(ctx, cacheGenerationMetrics); err != nil {
		t.Fatal(err)
	}
	if version() != initial+1 {
		t.Fatal("обычная смена ранга или цены сбросила двадцатиминутный кэш")
	}
	rollback := errors.New("откат")
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		record, err := tx.StoreCards().Get(ctx, "t1")
		if err != nil {
			return err
		}
		record.Header.TenantID = "t1"
		record.Header.Slug = "shop"
		record.Header.Name = "Магазин"
		record.CacheSignature = "should-rollback"
		if err := tx.StoreCards().Save(ctx, record); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) || version() != initial+1 {
		t.Fatal("откат проекции изменил кэш", err, version())
	}
	for i, query := range []string{
		cacheGenerationSignature,
		cacheGenerationLogo,
		cacheGenerationHide,
		cacheGenerationDelete,
	} {
		var args []any
		if i == 0 {
			args = []any{"eligible-product-image-changed"}
		}
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
		if version() != initial+int64(i)+2 {
			t.Fatal("публичное изменение не сменило поколение", i, version())
		}
	}
}

func TestStoreCardRedisFirstReadAfterVisibilityChange(t *testing.T) {
	pool := testPool(t)
	source := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	cached := cachedStoreCards(t, source, pool.Config().ConnConfig.Database+":")
	read := func() ports.ShopPage {
		t.Helper()
		var page ports.ShopPage
		if err := cached.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			var err error
			page, err = tx.StoreCards().List(ctx, "", 20, 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return page
	}
	if page := read(); page.Total != 0 {
		t.Fatal(page)
	}
	seedTenant(t, source, "t1", "shop", participant.TenantActive)
	if err := source.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		member, err := participant.New(tenant, "Магазин", at(0))
		if err != nil {
			return err
		}
		return tx.Participants().Add(ctx, member)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, cacheGenerationSeed); err != nil {
		t.Fatal(err)
	}
	if page := read(); page.Total != 1 || len(page.Items) != 1 {
		t.Fatal("новый магазин не появился в первом GET", page)
	}
	if _, err := pool.Exec(ctx, cacheGenerationHide); err != nil {
		t.Fatal(err)
	}
	if page := read(); page.Total != 0 || len(page.Items) != 0 {
		t.Fatal("скрытый магазин остался в Redis", page)
	}
}

func TestStoreCardHidesDisabledShopWithPendingDeltas(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	cached := cachedStoreCards(t, uow, pool.Config().ConnConfig.Database+":")
	clock := fake.NewClock(at(12))
	service := app.NewService(cached, clock, &fake.IDs{})
	seedSummarySeller(t, uow, clock)
	const size = 1000
	seedSummaryScale(t, pool, size)
	drainSummaryRollup(t, uow)
	refreshSummary(t, service)
	assertStoreCardLists(t, service, cached, 1) // Прогреваем общий, категорийный и адресный кэш.
	if _, err := service.Shop(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StorePageSummary(ctx, "t1"); err != nil {
		t.Fatal(err)
	}

	events := make([]appcatalog.ProductEvent, 0, size)
	for i := 1; i <= size; i++ {
		snapshot := summarySnapshot(ids.ProductID(fmt.Sprintf("p%06d", i)), "c1", "", 99, 0, 0)
		snapshot.At = at(2)
		snapshot.Revision++
		events = append(events, appcatalog.ProductEvent{Snapshot: snapshot})
	}
	if err := appcatalog.NewProjector(uow, clock, nil).ApplyProducts(ctx, events); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := pool.QueryRow(ctx, pendingStoreCardChanges).Scan(&pending); err != nil || pending != size {
		t.Fatalf("перед отключением ожидали %d дельт, получено %d: %v", size, pending, err)
	}
	if _, err := service.Disable(ctx, "t1", participant.Decision{Note: "Отключение"}); err != nil {
		t.Fatal(err)
	}
	refreshSummary(t, service)
	err := pool.QueryRow(ctx, pendingStoreCardChanges).Scan(&pending)
	if err != nil || pending <= 0 || pending >= size {
		t.Fatalf("после первого прохода должны остаться дельты, получено %d: %v", pending, err)
	}

	if _, err := service.Shop(ctx, "shop"); !errors.Is(err, participant.ErrNotFound) {
		t.Fatalf("страница отключённого магазина открывается: %v", err)
	}
	if _, err := service.StorePageSummary(ctx, "t1"); !errors.Is(err, participant.ErrNotFound) {
		t.Fatalf("публичная сводка отключённого магазина доступна: %v", err)
	}
	assertStoreCardLists(t, service, cached, 0)
}

func assertStoreCardLists(t *testing.T, service *app.Service, cached ports.UnitOfWork, want int) {
	t.Helper()
	ctx := context.Background()
	for _, category := range []string{"", "c1"} {
		page, err := service.Shops(ctx, category, 20, 0)
		if err != nil || page.Total != want || len(page.Items) != want {
			t.Fatalf("список магазинов для категории %q: %+v, %v", category, page, err)
		}
	}
	if err := cached.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		items, err := tx.StoreCards().BatchPublic(ctx, []ids.TenantID{"t1"})
		if err != nil {
			return err
		}
		if len(items) != want {
			return fmt.Errorf("список подписок: получено %d карточек вместо %d", len(items), want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func cachedStoreCards(t *testing.T, source ports.UnitOfWork, prefix string) ports.UnitOfWork {
	t.Helper()
	ctx := context.Background()
	address := os.Getenv("TEST_REDIS_URL")
	if address == "" {
		address = "redis://localhost:26379/0"
	}
	client, err := redis.Open(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	t.Cleanup(func() {
		keys, err := client.Keys(ctx, prefix+"*").Result()
		if err == nil && len(keys) != 0 {
			_ = client.Del(ctx, keys...).Err()
		}
	})
	return storecache.NewUnitOfWork(source, redis.NewStoreCardCache(client, prefix))
}
