//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const (
	seedOldPickupBuyer = `INSERT INTO buyers (id, phone, created_at)
        VALUES ('10000000-0000-4000-8000-000000000001', '79251234567', now())`
	seedOldPickupPurchase = `INSERT INTO checkouts (
        id, buyer_id, idempotency_key, status, carrier, pickup_point_id, delivery,
        goods_total, delivery_total, total, version, created_at, updated_at, confirmed_at
    ) VALUES ($1::uuid, '10000000-0000-4000-8000-000000000001', $1::text, $2, 'cdek', 'P1',
        '{"Carrier":"cdek","PickupPointCode":"P1"}', 10000, 0, 10000, 1, now(), now(), now())`
	seedOldPickupGroup = `INSERT INTO checkout_groups (
        checkout_id, tenant_id, external_checkout_id, lines, goods, quote_status, delivery_cost,
        min_days, max_days, quote_expires_at, order_progress
    ) VALUES ($1::uuid, 't1', $1::text, '[]', 10000, 'available', 1000, 2, 4, now() + interval '1 hour', $2)`
	seedHistoricalSecondPickup = `INSERT INTO checkout_groups (
        checkout_id, tenant_id, external_checkout_id, lines, goods, quote_status, delivery_cost,
        min_days, max_days, quote_expires_at, order_progress
    ) SELECT checkout_id, 't2', external_checkout_id || ':t2', lines, goods, quote_status, delivery_cost,
        min_days, max_days, quote_expires_at,
        '{"OrderID":"order-2","Delivery":{"Carrier":"yandex","PickupPointCode":"P2"}}'::jsonb
        FROM checkout_groups WHERE checkout_id = $1 AND tenant_id = 't1'`
	checkHistoricalPickup = `SELECT c.delivery IS NULL, c.total,
        count(DISTINCT g.order_progress->>'OrderID'), bool_or(g.order_progress ? 'Delivery')
        FROM checkouts c JOIN checkout_groups g ON g.checkout_id = c.id WHERE c.id = $1 GROUP BY c.id`
	checkMigratedPickup = `SELECT c.carrier IS NULL, c.confirmed_at IS NULL, g.quote_status IS NULL,
        g.order_progress ? 'Delivery', c.total, c.status FROM checkouts c JOIN checkout_groups g ON g.checkout_id = c.id
        WHERE c.id = $1`
)

func TestSinglePickupMigrationClearsDraftAndPreservesCreatedOrders(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := postgres.MigrateDownTo(ctx, pool, 59); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, seedOldPickupBuyer); err != nil {
		t.Fatal(err)
	}
	for index, status := range []string{"draft", "awaiting_payment", "confirmed"} {
		id := "20000000-0000-4000-8000-000000000001"
		progress := `{"Delivery":{"Carrier":"cdek","PickupPointCode":"P1"}}`
		if index == 2 {
			id = "20000000-0000-4000-8000-000000000003"
		}
		if index == 1 {
			id = "20000000-0000-4000-8000-000000000002"
			progress = `{"OrderID":"order-1","Delivery":{"Carrier":"cdek","PickupPointCode":"P1"}}`
		}
		if _, err := pool.Exec(ctx, seedOldPickupPurchase, id, status); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, seedOldPickupGroup, id, progress); err != nil {
			t.Fatal(err)
		}
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{
		"20000000-0000-4000-8000-000000000001",
		"20000000-0000-4000-8000-000000000002",
		"20000000-0000-4000-8000-000000000003",
	} {
		var selectionCleared, consentCleared, quoteCleared, groupSelection bool
		var total int64
		var status string
		err := pool.QueryRow(ctx, checkMigratedPickup, id).Scan(
			&selectionCleared,
			&consentCleared,
			&quoteCleared,
			&groupSelection,
			&total,
			&status,
		)
		draft := index != 1
		expectedStatus := "awaiting_payment"
		if draft {
			expectedStatus = "draft"
		}
		if err != nil || selectionCleared != draft || consentCleared != draft || quoteCleared != draft ||
			groupSelection || total != 10000 || status != expectedStatus {
			t.Fatal(index, selectionCleared, consentCleared, quoteCleared, groupSelection, total, err)
		}
	}
}

func TestSinglePickupMigrationDoesNotMislabelHistoricalParcels(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	if err := postgres.MigrateDownTo(ctx, pool, 59); err != nil {
		t.Fatal(err)
	}
	id := "20000000-0000-4000-8000-000000000003"
	if _, err := pool.Exec(ctx, seedOldPickupBuyer); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, seedOldPickupPurchase, id, "paid"); err != nil {
		t.Fatal(err)
	}
	progress := `{"OrderID":"order-1","Delivery":{"Carrier":"cdek","PickupPointCode":"P1"}}`
	if _, err := pool.Exec(ctx, seedOldPickupGroup, id, progress); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, seedHistoricalSecondPickup, id); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var noCommonPickup, groupSelection bool
	var total int64
	var orders int
	err := pool.QueryRow(ctx, checkHistoricalPickup, id).Scan(&noCommonPickup, &total, &orders, &groupSelection)
	if err != nil || !noCommonPickup || groupSelection || total != 10000 || orders != 2 {
		t.Fatal(noCommonPickup, total, orders, groupSelection, err)
	}
}
