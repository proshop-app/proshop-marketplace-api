//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func TestRetryStatsExcludeFirstLeasesAndQuarantine(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err := pool.Exec(
		ctx,
		`INSERT INTO outbox (
        kind, tenant_id, payload, attempts, next_at, created_at,
        last_error, unknown_failures, failed_at, failure_reason
    ) VALUES
        ('index', 't1', '{}', 3, $1::timestamptz + interval '1 day', $1::timestamptz - interval '2 hours',
            'network', NULL, NULL, NULL),
        ('index', 't2', '{}', 2, $1::timestamptz, $1::timestamptz - interval '1 hour', NULL, 1, NULL, NULL),
        ('status', 't1', '{}', 9, $1::timestamptz, $1::timestamptz - interval '30 minutes',
            'network', NULL, NULL, NULL),
        ('index', 't1', '{}', 0, $1::timestamptz, $1::timestamptz - interval '5 hours', NULL, NULL, NULL, NULL),
        ('index', 't1', '{}', 1, $1::timestamptz, $1::timestamptz - interval '6 hours', NULL, NULL, NULL, NULL),
        ('index', 't1', '{}', 2, $1::timestamptz, $1::timestamptz - interval '8 hours',
            'old', NULL, $1::timestamptz, 'invalid')`,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := postgres.NewTaskQueue(postgres.Single(pool)).RetryStats(ctx)
	if err != nil || len(stats) != 2 {
		t.Fatal(stats, err)
	}
	for _, item := range stats {
		count, oldest := int64(2), now.Add(-2*time.Hour)
		if item.Kind == "status" {
			count, oldest = 1, now.Add(-30*time.Minute)
		}
		if item.Count != count || !item.Oldest.Equal(oldest) {
			t.Fatal(item, count, oldest)
		}
	}
}
