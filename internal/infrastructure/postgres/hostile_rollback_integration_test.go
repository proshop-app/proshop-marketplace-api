//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

// Откат 00058 и запуск прежнего образа: старый scanParticipant читает free_delivery_from в int64.
func TestHostileDownMigrationIsNotReadableByOldCode(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO tenants (tenant_id, slug, status, event_at, version, updated_at)
		VALUES ('t1', 's1', 'active', now(), 1, now())`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO participants (tenant_id, slug, name, stage, enabled, awaiting_review,
		version, created_at, updated_at, delivery_settings, trust, approved_offers,
        delivery_valid, store_card, card_ready)
		VALUES ('t1', 's1', 'Магазин', 'connected', true, false, 1, now(), now(),
        '{}', 'probation', 0, false, '{}', false)`,
	); err != nil {
		t.Fatal(err)
	}
	if err := postgres.MigrateDownTo(ctx, pool, 57); err != nil {
		t.Fatal(err)
	}
	var nullable string
	if err := pool.QueryRow(
		ctx,
		`SELECT is_nullable || '/' || coalesce(column_default, '<нет>')
		FROM information_schema.columns WHERE table_name = 'participants' AND column_name = 'free_delivery_from'`,
	).
		Scan(&nullable); err != nil {
		t.Fatal(err)
	}
	var free int64
	err := pool.QueryRow(ctx, `SELECT free_delivery_from FROM participants WHERE tenant_id = 't1'`).Scan(&free)
	t.Logf(
		"после down 00058: nullable/default = %s; scan в int64: %v",
		nullable,
		err,
	)
	if err != nil {
		t.Fatalf("откат схемы не совместим с прежним кодом")
	}
}
