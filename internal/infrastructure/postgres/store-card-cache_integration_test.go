//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/storecache"
)

const (
	cacheGenerationSeed = `INSERT INTO store_cards (tenant_id,slug,name,offers,public,page_public,summary,header,card)
 VALUES ('t1','shop','Магазин',3,true,true,'{}','{"categories":[]}',
 '{"tenantId":"t1","slug":"shop","name":"Магазин","appearance":{"color":"#125F67"},"products":[]}')`
	cacheGenerationMetrics = `UPDATE store_cards SET offers = offers + 1,
 card = card || '{"offers":7,"fromPrice":500,"rating":{"value":5,"count":7},
 "products":[{"productId":"p1","offerId":"t1~p1","name":"Товар","image":null}]}'::jsonb
 WHERE tenant_id = 't1'`
	cacheGenerationSignature  = `UPDATE store_cards SET cache_signature = $1 WHERE tenant_id = 't1'`
	cacheGenerationAppearance = `UPDATE store_cards SET card =
 jsonb_set(card,'{appearance,color}','"#F3F3F3"') WHERE tenant_id = 't1'`
	cacheGenerationHide   = `UPDATE store_cards SET public = false, page_public = false WHERE tenant_id = 't1'`
	cacheGenerationDelete = `DELETE FROM store_cards WHERE tenant_id = 't1'`
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
		cacheGenerationAppearance,
		cacheGenerationHide,
		cacheGenerationDelete,
	} {
		var args []any
		if i == 0 {
			args = []any{"eligible-product-image-or-pins-changed"}
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
	address := os.Getenv("TEST_REDIS_URL")
	if address == "" {
		address = "redis://localhost:26379/0"
	}
	client, err := redis.Open(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := pool.Config().ConnConfig.Database + ":"
	t.Cleanup(func() {
		keys, err := client.Keys(ctx, prefix+"*").Result()
		if err == nil && len(keys) != 0 {
			_ = client.Del(ctx, keys...).Err()
		}
	})
	cached := storecache.NewUnitOfWork(source, redis.NewStoreCardCache(client, prefix))
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
