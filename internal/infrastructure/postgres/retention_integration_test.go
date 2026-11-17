//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/event"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func TestRetentionKeepsUnresolvedEventsAndRecentDecisions(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seed := `INSERT INTO parked_events
        (topic, kafka_partition, kafka_offset, message_key, headers, value, reason, error,
         attempts, parked_at, status, updated_at)
        SELECT 'topic', 0, n, '', '{}', '{}', 'invalid_payload', 'json', 1, $1, status, updated
        FROM (VALUES (1, 'parked', $1::timestamptz), (2, 'queued', $1::timestamptz),
                     (3, 'resolved', $1::timestamptz), (4, 'resolved', $2::timestamptz)) v(n, status, updated)`
	if _, err := pool.Exec(ctx, seed, at(0), at(50)); err != nil {
		t.Fatal(err)
	}
	result, err := postgres.NewRetention(pool).Purge(ctx, ports.PurgeRequest{
		ParkedBefore: at(10),
		Batch:        2,
	})
	if err != nil || result.Parked != 1 {
		t.Fatalf("удаляется только давно завершённое событие: %+v, %v", result, err)
	}
	service := messaging.NewEvents(postgres.NewUnitOfWork(pool), clock.System{}, nil)
	for _, status := range []string{event.Parked, event.Queued, event.Resolved} {
		page, err := service.List(ctx, operator.Principal{ID: "op1", Role: operator.Role}, ports.EventFilter{
			Status: status,
		})
		if err != nil || page.Total != 1 || len(page.Items) != 1 || !page.Items[0].ParkedAt.Equal(at(0)) {
			t.Fatalf("оператор видит число и исходный возраст %s: %+v, %v", status, page, err)
		}
	}
}
