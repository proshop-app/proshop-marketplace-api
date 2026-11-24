//go:build integration

package postgres_test

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

const seedOutboxPlan = `INSERT INTO outbox (
    kind, tenant_id, payload, attempts, next_at, created_at, failed_at, failure_reason
)
SELECT CASE WHEN n % 10000 = 0 THEN 'index' ELSE 'status' END,
    't' || ((n / 1000) % 1000)::text, '{}', CASE WHEN n % 100 = 0 THEN 1 ELSE 0 END,
    CASE WHEN n % 1000 = 1 THEN now() - interval '1 hour' ELSE now() + interval '1 day' END,
    now() - interval '1 day',
    CASE WHEN n % 100 = 0 THEN now() END,
    CASE WHEN n % 100 = 0 THEN 'test.invalid' END
FROM generate_series(1, 1000000) n`

// Планы проверяют именно SQL репозитория на миллионе намерений с 1% карантина.
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
	if _, err := conn.Exec(ctx, seedOutboxPlan); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `UPDATE outbox SET last_error = 'test.retry', attempts = 1 WHERE id % 10000 = 1`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "VACUUM ANALYZE outbox"); err != nil {
		t.Fatal(err)
	}
	var total, failed int
	err = conn.QueryRow(ctx, "SELECT count(*), count(failed_at) FROM outbox").Scan(&total, &failed)
	if err != nil || total != 1000000 || failed != 10000 {
		t.Fatal(total, failed, err)
	}
	tests := []struct {
		file     string
		constant string
		args     []any
		indexes  []string
	}{
		{
			file:     "outbox.go",
			constant: "claimTasks",
			args: []any{
				time.Now(),
				200,
				2,
				time.Now().Add(time.Minute),
				[]string{},
				[]string{},
				"null",
				"t5",
				[]string{"index"},
			},
			indexes: []string{"outbox_lane_due_idx"},
		},
		{
			file:     "outbox-batch.go",
			constant: "selectReadyLanes",
			args:     []any{time.Now()},
			indexes:  []string{"outbox_lane_due_idx"},
		},
		{
			file:     "outbox-failures.go",
			constant: "selectFailedTasks",
			args:     []any{0, 100},
			indexes:  []string{"outbox_quarantine_id_idx"},
		},
		{
			file:     "outbox-requeue.go",
			constant: "requeueFailedTasks",
			args:     []any{0, 0, "index", 100, time.Now()},
			indexes:  []string{"outbox_quarantine_kind_id_idx"},
		},
		{
			file:     "outbox-retry-stats.go",
			constant: "selectOutboxRetryStats",
			indexes:  []string{"outbox_retry_created_idx"},
		},
	}
	for _, test := range tests {
		t.Run(test.constant, func(t *testing.T) {
			source, err := os.ReadFile(test.file)
			if err != nil {
				t.Fatal(err)
			}
			pattern := regexp.MustCompile("(?s)\\b" + test.constant + " = `([^`]+)`")
			match := pattern.FindSubmatch(source)
			if len(match) != 2 {
				t.Fatal("SQL не найден", test.constant)
			}
			tx, err := conn.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+string(match[1]), test.args...)
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
