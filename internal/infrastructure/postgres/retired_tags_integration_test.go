//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const retiredTagsFixture = `
    INSERT INTO catalog_tags (tenant_id, tag_id, revision, deleted, value, version, updated_at)
    VALUES ('t1', 'tag-1', 1, false, 'Метка', 1, now());
    INSERT INTO catalog_products
        (tenant_id, product_id, revision, deleted, card, price, projected_at, version, updated_at)
    VALUES ('t1', 'p1', 1, false, '{"name":"Товар","tags":["Метка"]}', 100, now(), 1, now());`

func TestRetiredTagsMigrationRemovesStorageAndPreservesProduct(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)

	if err := postgres.MigrateDown(ctx, pool); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, retiredTagsFixture); err != nil {
		t.Fatal(err)
	}

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var tableGone, tagsGone bool
	var name string
	err := pool.QueryRow(ctx, `SELECT to_regclass('catalog_tags') IS NULL`).Scan(&tableGone)

	if err != nil || !tableGone {
		t.Fatalf("таблица тегов не удалена: %v %v", tableGone, err)
	}
	err = pool.QueryRow(ctx, `SELECT NOT (card ? 'tags'), card->>'name'
        FROM catalog_products WHERE tenant_id = 't1' AND product_id = 'p1'`).Scan(&tagsGone, &name)

	if err != nil || !tagsGone || name != "Товар" {
		t.Fatalf("очистка карточки: tagsGone=%v name=%s err=%v", tagsGone, name, err)
	}
}
