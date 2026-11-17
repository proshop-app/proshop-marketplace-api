//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const (
	reportPlanParticipants = `UPDATE participants SET delivery_valid = true,
 subsidy = '{"enabled":true}', delivery_settings = '{"regions":["r1"],"carriers":["c1"]}'`
	reportPlanProducts = `INSERT INTO catalog_products
 (tenant_id, product_id, revision, deleted, card, price, available, projected_at,
 version, updated_at, price_profiles)
 SELECT tenant_id, 'p' || lpad(n::text, 6, '0'), 1, false,
 jsonb_build_object('name', 'Товар', 'visible', true, 'assets', '[]'::jsonb,
 'description', repeat(md5(tenant_id || n::text), 16),
 'modelKey', CASE WHEN n % 10 = 0 THEN '' WHEN n % 2 = 0 THEN 'shared:' || n::text
 ELSE tenant_id || ':' || n::text END,
 'modelKeySource', CASE WHEN n % 10 = 0 THEN '' ELSE 'identity' END,
 'publication', jsonb_build_object('published', true, 'categoryPath', jsonb_build_array('d' || (n % 10)::text),
 'schema', 's' || (n % 20)::text, 'issues', '[]'::jsonb)),
 100, 1, $1, 1, $1, '[{"key":"marketplace:default"}]'::jsonb
 FROM tenants CROSS JOIN generate_series(1, 2100) n`
	reportPlanAnalyze     = `ANALYZE catalog_products; ANALYZE participants; ANALYZE tenants`
	reportPlanEnvironment = `SELECT current_setting('server_version'), current_setting('work_mem'),
 pg_total_relation_size('catalog_products')`
)

type reportPlanExplain struct {
	Plan struct {
		ActualRows int `json:"Actual Rows"`
		SharedHit  int `json:"Shared Hit Blocks"`
		SharedRead int `json:"Shared Read Blocks"`
		TempRead   int `json:"Temp Read Blocks"`
		TempWrite  int `json:"Temp Written Blocks"`
	} `json:"Plan"`
	ExecutionTime float64 `json:"Execution Time"`
}

// Явный замер полного диагностического отчёта: MODEL_KEY_REPORT_PLAN=1.
// Большая фикстура не нужна обычному прогону репозиториев.
func TestModelKeyReportPlan(t *testing.T) {
	if os.Getenv("MODEL_KEY_REPORT_PLAN") != "1" {
		t.Skip("замер 2,1 млн товаров: MODEL_KEY_REPORT_PLAN=1")
	}
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{sql: offerPlanSeedTenants, args: []any{1000, at(0)}},
		{sql: offerPlanSeedParticipants, args: []any{at(0)}},
		{sql: reportPlanParticipants},
		{sql: reportPlanProducts, args: []any{at(0)}},
		{sql: reportPlanAnalyze},
	} {
		if _, err := pool.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatal(err)
		}
	}
	var version, workMem string
	var bytes int64
	if err := pool.QueryRow(ctx, reportPlanEnvironment).Scan(&version, &workMem, &bytes); err != nil {
		t.Fatal(err)
	}
	t.Logf("PostgreSQL=%s work_mem=%s catalog_bytes=%d shops=1000 products=2100000", version, workMem, bytes)
	trace := &readPlanTrace{match: "GROUP BY GROUPING SETS"}
	config := pool.Config()
	config.ConnConfig.Tracer = trace
	config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	reader, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	started := time.Now()
	report, err := postgres.NewModelKeyStats(reader).Report(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, count := range report.Counts {
		if count.Direction == "" && count.Schema == "" {
			total += count.Offers
		}
	}
	if total != 2100000 || trace.sql == "" {
		t.Fatalf("неполная агрегация: total=%d sql=%q", total, trace.sql)
	}
	t.Logf("Report(false)=%s total=%d groups=%d", time.Since(started), total, len(report.Counts))
	logModelKeyReportPlan(ctx, t, reader, trace.sql)
}

func logModelKeyReportPlan(ctx context.Context, t *testing.T, pool *pgxpool.Pool, query string) {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var results []reportPlanExplain
	if err := json.Unmarshal(raw, &results); err != nil || len(results) != 1 {
		t.Fatalf("план: %s; ошибка: %v", raw, err)
	}
	r := results[0]
	t.Logf(
		"EXPLAIN rows=%d hit/read=%d/%d temp_read/write=%d/%d time=%.3fms",
		r.Plan.ActualRows,
		r.Plan.SharedHit,
		r.Plan.SharedRead,
		r.Plan.TempRead,
		r.Plan.TempWrite,
		r.ExecutionTime,
	)
	t.Logf("EXPLAIN JSON: %s", raw)
}
