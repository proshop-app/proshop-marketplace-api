//go:build integration

package postgres_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const seedOutboxPlan = `INSERT INTO outbox (
    kind, tenant_id, payload, attempts, next_at, created_at, failed_at, failure_reason
)
SELECT CASE WHEN n % 10000 = 0 OR n % 1000 = 9 THEN 'checkout.create_order'
        WHEN n % 10 < 8 THEN 'index' ELSE 'status' END,
    't' || ((n - 1) / 100)::text, '{}', CASE WHEN n % 100 = 0 THEN 1 ELSE 0 END,
    CASE WHEN n % 1000 BETWEEN 1 AND 11
        THEN $1::timestamptz - interval '1 hour' ELSE $1::timestamptz + interval '1 day' END,
    $1::timestamptz - interval '1 day',
    CASE WHEN n % 100 = 0 THEN $1::timestamptz END,
    CASE WHEN n % 100 = 0 THEN 'test.invalid' END
FROM generate_series(1, 1000000) n`

const countOutboxPlanFixture = "SELECT count(*), count(failed_at), count(DISTINCT tenant_id) FROM outbox"

// Планы проверяют SQL репозитория на миллионе намерений 10 тысяч магазинов с 1% карантина.
// Время выводится, но не является порогом CI: оно зависит от оборудования.
func TestOutboxMillionTaskPlans(t *testing.T) {
	if os.Getenv("OUTBOX_PLAN_TEST") != "1" {
		t.Skip("для миллионного EXPLAIN нужен OUTBOX_PLAN_TEST=1")
	}
	pool := testPool(t)
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SET statement_timeout = '5min'"); err != nil {
		t.Fatal(err)
	}
	now := at(0)
	if _, err := conn.Exec(ctx, seedOutboxPlan, now); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `UPDATE outbox SET last_error = 'test.retry', attempts = 1 WHERE id % 10000 = 1`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "VACUUM ANALYZE outbox"); err != nil {
		t.Fatal(err)
	}
	var total, failed, tenants int
	err = conn.QueryRow(ctx, countOutboxPlanFixture).Scan(&total, &failed, &tenants)
	if err != nil || total != 1000000 || failed != 10000 || tenants != 10000 {
		t.Fatal(total, failed, tenants, err)
	}
	tests := []struct {
		name    string
		query   string
		args    []any
		indexes []string
	}{
		{
			name:  "claimTasks",
			query: postgres.ClaimTasksPlanSQL,
			args: []any{
				now,
				200,
				2,
				now.Add(time.Minute),
				[]string{},
				[]string{},
				"null",
				"t5",
				[]string{},
				ports.ClaimLaneVisits,
				false,
				[]string{},
				"offer-index",
				[]string{"index"},
			},
			indexes: []string{"outbox_lane_due_idx"},
		},
		{
			name:    "selectReadyLanes",
			query:   postgres.ReadyLanesPlanSQL,
			args:    []any{now},
			indexes: []string{"outbox_lane_due_idx"},
		},
		{
			name:    "selectFailedTasks",
			query:   postgres.FailedTasksPlanSQL,
			args:    []any{0, 100},
			indexes: []string{"outbox_quarantine_id_idx"},
		},
		{
			name:    "requeueFailedTasks",
			query:   postgres.RequeueFailedPlanSQL,
			args:    []any{0, 0, "checkout.create_order", 100, now},
			indexes: []string{"outbox_quarantine_kind_id_idx"},
		},
		{
			name:    "selectOutboxRetryStats",
			query:   postgres.OutboxRetryStatsPlanSQL,
			indexes: []string{"outbox_retry_created_idx"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx, err := conn.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+test.query, test.args...)
			if err != nil {
				t.Fatal(err)
			}
			var plan strings.Builder
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				plan.WriteString(line + "\n")
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			t.Log("\n" + plan.String())
			for _, index := range test.indexes {
				if !strings.Contains(plan.String(), index) {
					t.Error("план не использует", index)
				}
			}
		})
	}
}
