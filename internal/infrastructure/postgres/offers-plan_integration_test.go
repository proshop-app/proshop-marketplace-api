//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const (
	offerPlanProductsPerTenant = 1000
	offerPlanSeedTenants       = `
		INSERT INTO tenants (tenant_id, slug, status, event_at, version, updated_at)
		SELECT 't' || lpad(n::text, 6, '0'), 't' || lpad(n::text, 6, '0'), 'active', $2, 1, $2
		FROM generate_series(1, $1::integer) n`
	offerPlanSeedParticipants = `
		INSERT INTO participants (tenant_id, slug, name, stage, enabled, version, created_at, updated_at)
		SELECT tenant_id, slug, 'Магазин', 'connected', true, 1, $1, $1 FROM tenants`
	offerPlanSeedProducts = `
		INSERT INTO catalog_products (
			tenant_id,
			product_id,
			revision,
			deleted,
			card,
			price,
			available,
			projected_at,
			version,
			updated_at
		)
		SELECT tenant_id, 'p' || lpad(n::text, 6, '0'), 1, false,
			'{"name":"Товар","visible":true,"assets":[],"publication":{}}'::jsonb,
			100, 1, $2, 1, $2
		FROM tenants CROSS JOIN generate_series(1, $1::integer) n`
	offerPlanSeedSignals = `
		INSERT INTO offers (tenant_id, product_id, available, stock_at, price, price_at, version, updated_at)
		SELECT tenant_id, product_id, 1, $1, 100, $1, 1, $1 FROM catalog_products`
	offerPlanSeedVerdicts = `
		INSERT INTO moderation_verdicts (
			tenant_id,
			product_id,
			revision,
			rules_revision,
			taxonomy_version,
			status,
			reasons,
			decided_by,
			version,
			updated_at
		)
		SELECT tenant_id, product_id, 1, 1, 1, 'allowed', '[]'::jsonb, 'rule', 1, $1
		FROM catalog_products WHERE right(product_id, 1) = '0'`
	offerPlanAnalyze = `ANALYZE tenants; ANALYZE participants; ANALYZE catalog_products;
		ANALYZE offers; ANALYZE moderation_verdicts`
	offerPlanEnvironment   = `SELECT version(), current_setting('shared_buffers'), current_setting('work_mem')`
	offerPlanSparseChanges = `
		UPDATE catalog_products SET updated_at = $2 WHERE tenant_id = $1 AND product_id = 'p001000';
		UPDATE offers SET updated_at = $2 WHERE tenant_id = $1 AND product_id = 'p000999';
		UPDATE moderation_verdicts SET updated_at = $2 WHERE tenant_id = $1 AND product_id = 'p000990'`
	offerPlanParticipantChange = `UPDATE participants SET updated_at = $2 WHERE tenant_id = $1`
	offerPlanTenantChange      = `UPDATE tenants SET updated_at = $2 WHERE tenant_id = $1`
	offerPlanDenseChanges      = `UPDATE catalog_products SET updated_at = $2 WHERE tenant_id = $1`
	offerPlanExplain           = `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) `
)

type offerPlanQueryTrace struct {
	sql  string
	args []any
}

type offerPlanResult struct {
	Plan struct {
		ActualRows int `json:"Actual Rows"`
	} `json:"Plan"`
}

func (trace *offerPlanQueryTrace) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, "OR mv.updated_at >= $3") {
		trace.sql, trace.args = data.SQL, data.Args
	}
	return ctx
}

func (*offerPlanQueryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// TestChangedOffersPlan запускается отдельно: OFFERS_PLAN_TEST=1 go test -tags integration
// ./internal/infrastructure/postgres -run '^TestChangedOffersPlan$' -count=1 -v.
// По умолчанию 100 000 товаров; OFFERS_PLAN_TENANTS=1000 увеличивает набор до миллиона.
// Время и планы только выводятся: пороги зависят от оборудования и не годятся для CI.
func TestChangedOffersPlan(t *testing.T) {
	if os.Getenv("OFFERS_PLAN_TEST") != "1" {
		t.Skip("для нагрузочного EXPLAIN нужен OFFERS_PLAN_TEST=1")
	}

	tenants := 100
	if raw := os.Getenv("OFFERS_PLAN_TENANTS"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 999999 {
			t.Fatal("OFFERS_PLAN_TENANTS должен быть в диапазоне 1..999999")
		}
		tenants = value
	}

	pool := testPool(t)
	ctx := context.Background()
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	trace := captureChangedOffersQuery(t, pool, since)
	seedOfferPlan(t, pool, tenants, since.Add(-time.Hour))

	var version, sharedBuffers, workMem string
	if err := pool.QueryRow(ctx, offerPlanEnvironment).Scan(&version, &sharedBuffers, &workMem); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s; shared_buffers=%s; work_mem=%s", version, sharedBuffers, workMem)
	t.Logf(
		"товары=%d; сигналы=%d; вердикты=%d; участники=%d",
		tenants*offerPlanProductsPerTenant,
		tenants*offerPlanProductsPerTenant,
		tenants*offerPlanProductsPerTenant/10,
		tenants,
	)

	lastTenant := fmt.Sprintf("t%06d", tenants)
	for _, tc := range []struct {
		name string
		sql  string
		id   string
		rows int
	}{
		{
			name: "unchanged",
			rows: 0,
		},
		{
			name: "sparse_changes_at_end",
			sql:  offerPlanSparseChanges,
			id:   lastTenant,
			rows: 3,
		},
		{
			name: "participant_changed_at_end",
			sql:  offerPlanParticipantChange,
			id:   lastTenant,
			rows: 100,
		},
		{
			name: "tenant_changed_at_end",
			sql:  offerPlanTenantChange,
			id:   lastTenant,
			rows: 100,
		},
		{
			name: "dense_changes_at_start",
			sql:  offerPlanDenseChanges,
			id:   "t000001",
			rows: 100,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()

			if tc.sql != "" {
				// Несколько UPDATE с параметрами требуют простого протокола pgx.
				_, err := tx.Exec(ctx, tc.sql, pgx.QueryExecModeSimpleProtocol, tc.id, since)
				if err != nil {
					t.Fatal(err)
				}
			}

			var plan []byte
			if err := tx.QueryRow(ctx, offerPlanExplain+trace.sql, trace.args...).Scan(&plan); err != nil {
				t.Fatal(err)
			}
			var result []offerPlanResult
			if err := json.Unmarshal(plan, &result); err != nil {
				t.Fatal(err)
			}
			if len(result) != 1 || result[0].Plan.ActualRows != tc.rows {
				t.Fatalf("ожидалось %d строк; план: %s", tc.rows, plan)
			}
			t.Logf("EXPLAIN (ANALYZE, BUFFERS):\n%s", plan)
		})
	}
}

func captureChangedOffersQuery(t *testing.T, pool *pgxpool.Pool, since time.Time) *offerPlanQueryTrace {
	t.Helper()

	ctx := context.Background()
	trace := &offerPlanQueryTrace{}
	config := pool.Config()
	config.ConnConfig.Tracer = trace
	tracedPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer tracedPool.Close()

	err = postgres.NewUnitOfWork(tracedPool).Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		_, err := tx.Offers().ChangedSince(ctx, since, ports.OfferKey{}, 100)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if trace.sql == "" {
		t.Fatal("не перехвачен SQL ChangedSince")
	}
	return trace
}

func seedOfferPlan(t *testing.T, pool *pgxpool.Pool, tenants int, at time.Time) {
	t.Helper()

	ctx := context.Background()
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{
			sql:  offerPlanSeedTenants,
			args: []any{tenants, at},
		},
		{
			sql:  offerPlanSeedParticipants,
			args: []any{at},
		},
		{
			sql:  offerPlanSeedProducts,
			args: []any{offerPlanProductsPerTenant, at},
		},
		{
			sql:  offerPlanSeedSignals,
			args: []any{at},
		},
		{
			sql:  offerPlanSeedVerdicts,
			args: []any{at},
		},
		{
			sql: offerPlanAnalyze,
		},
	} {
		if _, err := pool.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatal(err)
		}
	}
}
