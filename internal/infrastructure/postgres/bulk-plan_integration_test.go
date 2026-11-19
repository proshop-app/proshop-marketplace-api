//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	domain "github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
)

const bulkMillionCases = `INSERT INTO moderation_cases
    (id, tenant_id, product_id, revision, source, status, reasons, note, opened_at, version)
    SELECT md5(n::text)::uuid, 't' || (n % 1000), 'p' || n, 1,
        CASE WHEN n % 3 = 0 THEN 'operator' ELSE 'rule' END, 'open',
        CASE WHEN n % 100 = 0 THEN '[{"key":"moderation.probation"}]'::jsonb
            ELSE '[{"key":"moderation.manual_review"}]'::jsonb END, '', now(), 1
    FROM generate_series(1, 1000000) n`
const bulkWholePlan = `EXPLAIN (ANALYZE, BUFFERS) SELECT id, version FROM moderation_cases
    WHERE status = 'open' AND id > '00000000-0000-0000-0000-000000000000'
    ORDER BY id LIMIT 200`
const bulkSourcePlan = `EXPLAIN (ANALYZE, BUFFERS) SELECT id, version FROM moderation_cases
    WHERE status = 'open' AND source = 'operator' AND id > '00000000-0000-0000-0000-000000000000'
    ORDER BY id LIMIT 200`
const bulkTenantPlan = `EXPLAIN (ANALYZE, BUFFERS) SELECT id, version FROM moderation_cases
    WHERE status = 'open' AND tenant_id = 't0'
        AND moderation_reason_keys(reasons) = ARRAY['moderation.probation']::text[]
        AND id > '00000000-0000-0000-0000-000000000000'
    ORDER BY id LIMIT 200`

const bulkTenantAnyPlan = `EXPLAIN (ANALYZE, BUFFERS) SELECT id, version FROM moderation_cases
    WHERE status = 'open' AND tenant_id = 't0'
        AND id > '00000000-0000-0000-0000-000000000000'
    ORDER BY id LIMIT 200`
const bulkReasonCountPlan = `EXPLAIN (ANALYZE, BUFFERS) SELECT count(*), count(DISTINCT tenant_id)
    FROM moderation_cases WHERE status = 'open'
        AND moderation_reason_keys(reasons) @> ARRAY['moderation.probation']::text[]`

func TestBulkMillionCasePlansUseKeysetIndexes(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	// Только загрузка большого тестового набора; рабочий LIMIT 200 остаётся под обычным сроком.
	if _, err := conn.Exec(ctx, "SET statement_timeout = '180s'"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, bulkMillionCases); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "ANALYZE moderation_cases"); err != nil {
		t.Fatal(err)
	}
	// Подтверждение обязано работать с обычным сроком запроса, не со сроком загрузки фикстуры.
	service := moderation.NewService(postgres.NewUnitOfWork(pool), clock.System{}, random.IDs{}, moderation.Config{})
	confirmations := []struct {
		selector domain.BulkSelector
		expected domain.BulkCounts
	}{
		{
			selector: domain.BulkSelector{},
			expected: domain.BulkCounts{Total: 1000000, Tenants: 1000, OtherReasons: 990000},
		},
		{
			selector: domain.BulkSelector{Reason: "moderation.probation"},
			expected: domain.BulkCounts{Total: 10000, Tenants: 10},
		},
		{
			selector: domain.BulkSelector{Reason: "moderation.manual_review"},
			expected: domain.BulkCounts{Total: 990000, Tenants: 990, OtherReasons: 990000},
		},
	}
	for _, check := range confirmations {
		started := time.Now()
		counts, err := service.BulkCount(ctx, check.selector)
		if err != nil || counts != check.expected {
			t.Fatal("счёт подтверждения на миллионе дел", check.selector, counts, err)
		}
		t.Log("confirmation", check.selector, counts, time.Since(started))
	}

	checks := []struct {
		query string
		index string
		count bool
	}{
		{query: bulkWholePlan, index: "moderation_case_open_id"},
		{query: bulkSourcePlan, index: "moderation_case_source_open_id"},
		{query: bulkTenantPlan, index: "moderation_case_probation"},
		{query: bulkTenantAnyPlan, index: "moderation_case_tenant_open"},
		{query: bulkReasonCountPlan, index: "moderation_case_open_reasons", count: true},
	}
	for _, check := range checks {
		query := check.query
		rows, err := conn.Query(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		plan := ""
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan += line + "\n"
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		t.Log(query, "\n", plan)
		if !strings.Contains(plan, check.index) {
			t.Fatal("индекс без подтверждённого запроса", check.index, plan)
		}
		if check.count {
			continue
		}
		indexed := strings.Contains(plan, "Index Scan") && strings.Contains(plan, "rows=200")
		if strings.Contains(plan, "Sort") || !indexed {
			t.Fatal("нужна индексная страница без сортировки", plan)
		}
	}
}
