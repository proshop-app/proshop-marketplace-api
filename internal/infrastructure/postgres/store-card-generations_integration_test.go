//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	seedCacheVersions       = `INSERT INTO store_card_cache_versions(tenant_id,version) VALUES ('t1',2),('t2',3)`
	changeOtherCacheVersion = `UPDATE store_card_cache_versions SET version=version+1 WHERE tenant_id='t2'`
)

func TestStoreCardCacheVersionOnlyUsesRequestedTenants(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, seedCacheVersions); err != nil {
		t.Fatal(err)
	}
	uow := postgres.NewUnitOfWork(pool)
	version := func(tenants ...ids.TenantID) int64 {
		t.Helper()
		var value int64
		err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			var err error
			value, err = tx.StoreCards().CacheVersion(ctx, tenants...)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	global := version()
	if version("t1") != 2 || version([]ids.TenantID{}...) != 0 {
		t.Fatal("неверная область поколений")
	}
	if _, err := pool.Exec(ctx, changeOtherCacheVersion); err != nil {
		t.Fatal(err)
	}
	if version("t1") != 2 || version("t1", "t2") != 6 || version() != global {
		t.Fatal("чужое обновление изменило адресное поколение")
	}
}

func TestStoreCardGenerationsMigrationDoesNotReviveDeletedCacheKey(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedReadParticipant(t, postgres.NewUnitOfWork(pool), "t1", false)
	if _, err := pool.Exec(ctx, cacheGenerationSeed); err != nil {
		t.Fatal(err)
	}
	before := cacheMetadata(t, pool)
	if _, err := pool.Exec(ctx, cacheGenerationDelete); err != nil {
		t.Fatal(err)
	}
	if err := postgres.MigrateDownTo(ctx, pool, 52); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, cacheGenerationSeed); err != nil {
		t.Fatal(err)
	}
	if after := cacheMetadata(t, pool); after[0] <= before[0] || after[1] <= before[1] || after[2] != 1 {
		t.Fatalf("повтор миграции/создание магазина вернули прежний ключ: %v → %v", before, after)
	}
}
