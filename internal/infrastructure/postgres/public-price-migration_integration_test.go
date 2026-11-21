//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const (
	seedPublicPriceMigration = `INSERT INTO catalog_products
        (tenant_id, product_id, revision, deleted, card, price, price_profiles, projected_at, version, updated_at)
        VALUES
        ('t1', 'priced', 1, false, '{}', 900,
            '[{"key":"marketplace:default","price":900,"effectivePrice":700}]', now(), 1, now()),
        ('t1', 'deleted', 1, true, NULL, 900,
            '[{"key":"marketplace:default","price":900,"effectivePrice":700}]', now(), 1, now()),
        ('t1', 'missing', 1, false, '{}', 900, '[]', now(), 1, now()),
        ('t1', 'empty', 1, true, NULL, NULL, '[]', now(), 1, now())`
	checkPublicPriceMigration = `SELECT count(*) FROM catalog_products WHERE
        (product_id = 'priced' AND price = 700 AND base_price = 900) OR
        (product_id IN ('deleted', 'missing', 'empty') AND price IS NULL AND base_price IS NULL)`
	checkPublicPriceIndexes = `SELECT count(*) FROM pg_index i
        JOIN pg_class c ON c.oid = i.indexrelid
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = current_schema() AND i.indisvalid AND c.relname IN
        ('catalog_recommendations_category', 'catalog_recommendations_popular', 'catalog_recommendations_tenant_rank')`
)

func TestPublicPriceMigrationRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := postgres.MigrateDownTo(ctx, pool, 63); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, seedPublicPriceMigration); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := postgres.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := pool.QueryRow(ctx, checkPublicPriceMigration).Scan(&count); err != nil || count != 4 {
			t.Fatal(count, err)
		}
		if err := pool.QueryRow(ctx, checkPublicPriceIndexes).Scan(&count); err != nil || count != 3 {
			t.Fatal(count, err)
		}
		if err := postgres.MigrateDownTo(ctx, pool, 63); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, checkPublicPriceIndexes).Scan(&count); err != nil || count != 3 {
			t.Fatal(count, err)
		}
	}
}
