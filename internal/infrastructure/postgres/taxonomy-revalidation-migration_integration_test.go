//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
)

const existingTaxonomySnapshotSQL = `INSERT INTO taxonomy_versions (version, definition)
 VALUES (7, '{"version":7,"categories":[]}')`

const existingTaxonomyHeadSQL = `UPDATE taxonomy_head
 SET version = 7,
 definition = '{"version":7,"categories":[]}'`

func TestTaxonomyRevalidationMigrationExistingInstallation(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)
	if err := postgres.MigrateDownTo(ctx, pool, 43); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, existingTaxonomySnapshotSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, existingTaxonomyHeadSQL); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := postgres.NewUnitOfWork(pool).Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		progress, err := tx.TaxonomyRevalidation().LockProgress(ctx)
		if err != nil {
			return err
		}
		if progress != (ports.TaxonomyProgress{CardsVersion: 7, Done: true}) {
			t.Fatalf("неверный начальный прогресс: %+v", progress)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
