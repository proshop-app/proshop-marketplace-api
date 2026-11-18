//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const checkoutPricingColumnCount = `SELECT count(*) FROM information_schema.columns
    WHERE table_schema = 'public' AND (
    (table_name = 'checkout_groups' AND column_name IN ('subsidy', 'buyer_delivery'))
    OR (table_name = 'participants' AND column_name IN ('subsidy', 'free_delivery_from')))`

func TestCheckoutPricingMigrationDoesNotWaitForWriters(t *testing.T) {
	for _, table := range []string{"participants", "checkout_groups"} {
		t.Run(table, func(t *testing.T) { testPricingMigrationDoesNotWait(t, table) })
	}
}

func testPricingMigrationDoesNotWait(t *testing.T, table string) {
	pool := testPool(t)
	ctx := context.Background()
	if err := postgres.MigrateDownTo(ctx, pool, 57); err != nil {
		t.Fatal(err)
	}
	writer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(ctx) }()
	if _, err := writer.Exec(ctx, "LOCK TABLE "+table+" IN ROW EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	err = postgres.Migrate(ctx, pool)
	var conflict *pgconn.PgError
	if !errors.As(err, &conflict) || conflict.Code != "55P03" {
		t.Fatal("миграция должна сразу отказать при занятой таблице", err)
	}
	var count int
	if err := pool.QueryRow(ctx, checkoutPricingColumnCount).Scan(&count); err != nil || count != 4 {
		t.Fatal("неуспешная миграция изменила схему", count, err)
	}
	if _, err := writer.Exec(ctx, "UPDATE checkout_groups SET goods = goods"); err != nil {
		t.Fatal("миграция помешала продолжающейся записи", err)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := postgres.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, checkoutPricingColumnCount).Scan(&count); err != nil || count != 0 {
		t.Fatal("старые столбцы остались", count, err)
	}
}
