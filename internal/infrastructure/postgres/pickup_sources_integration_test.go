//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const seedPickupTenants = `INSERT INTO tenants (tenant_id, slug, status, event_at, version, updated_at)
    SELECT 't' || lpad(n::text, 5, '0'), 'shop-' || n, 'active', now(), 1, now()
    FROM generate_series(1, $1::int) n`

const seedPickupParticipants = `INSERT INTO participants
    (tenant_id, slug, name, stage, enabled, version, created_at, updated_at, delivery_settings, delivery_valid)
    SELECT tenant_id, slug, 'Магазин', 'connected', true, 1, now(), now(),
    '{"regions":["RU-MOW"],"carriers":["cdek"]}'::jsonb, true FROM tenants`

const explainPickupSourcePage = `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
    SELECT p.tenant_id FROM participants p JOIN tenants t ON t.tenant_id = p.tenant_id
    WHERE p.stage = 'connected' AND p.enabled AND p.delivery_valid AND t.status = 'active'
    AND p.tenant_id > 't00004'
    ORDER BY p.tenant_id LIMIT 1`

const disablePickupSource = `UPDATE participants SET enabled = false, stage = 'disconnected' WHERE tenant_id = 't00005'`
const suspendPickupSource = `UPDATE tenants SET status = 'suspended' WHERE tenant_id = 't00006'`
const disconnectPickupSource = `UPDATE participants SET delivery_valid = false WHERE tenant_id = 't00007'`

const invalidDeliveryPickupSource = `UPDATE participants SET delivery_valid = false WHERE tenant_id = 't00008'`

func TestPickupSourcesUseBoundedIndexedPages(t *testing.T) {
	for _, shops := range []int{1000, 5000} {
		t.Run(fmt.Sprint(shops), func(t *testing.T) { testPickupSourcePage(t, shops) })
	}
}

func testPickupSourcePage(t *testing.T, shops int) {
	pool := testPool(t)
	ctx := context.Background()
	for _, statement := range []string{seedPickupTenants, seedPickupParticipants} {
		args := []any{}
		if statement == seedPickupTenants {
			args = append(args, shops)
		}
		if _, err := pool.Exec(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, "ANALYZE participants; ANALYZE tenants"); err != nil {
		t.Fatal(err)
	}
	config := pool.Config()
	counter := &checkoutQueryCounter{}
	counter.reset()
	config.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	uow := postgres.NewUnitOfWork(traced)
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		page, err := tx.Participants().PickupSources(ctx, "cdek", "t00004", 1)
		if err == nil && (len(page) != 1 || page[0] != "t00005") {
			t.Fatal(page)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if counter.counts["SELECT"] != 1 {
		t.Fatal(counter.counts)
	}
	counter.log(t, fmt.Sprintf("источники ПВЗ при %d магазинах", shops))
	var plan []byte
	if err := pool.QueryRow(ctx, explainPickupSourcePage).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plan), "participants_pickup_visible") {
		t.Fatal("план не использует частичный индекс видимых магазинов", string(plan))
	}
	var pretty bytes.Buffer
	if err := json.Compact(&pretty, plan); err != nil {
		t.Fatal(err)
	}
	t.Logf("Postgres %d магазинов: %s", shops, pretty.String())
	for _, statement := range []string{
		disablePickupSource,
		suspendPickupSource,
		disconnectPickupSource,
		invalidDeliveryPickupSource,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		page, err := tx.Participants().PickupSources(ctx, "cdek", "t00004", 3)
		if err == nil && (len(page) != 3 || page[0] != "t00009") {
			t.Fatal("отключение участника/службы и магазина применяется сразу", page)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
