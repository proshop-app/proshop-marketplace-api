//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const pendingReferenceState = `
	SELECT done_at IS NULL, next_at = 'infinity'::timestamptz,
		last_error, payload->>'revisionTo'
	FROM outbox WHERE id = $1`

func TestRetiredReferenceTasksAreExportedAndParkedWithoutFalseAcknowledgment(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	err := postgres.NewUnitOfWork(pool).Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for _, kind := range []string{"reference.changed", "active-kind"} {
			if err := tx.Outbox().Enqueue(ctx, ports.NewTask{
				Kind:     kind,
				TenantID: "t1",
				Payload:  []byte(`{"revisionTo":12}`),
				At:       now,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	queue := postgres.NewTaskQueue(pool)
	claimed, err := queue.Claim(ctx, ports.ClaimRequest{
		Now:        now,
		Limit:      10,
		PerTenant:  10,
		LeaseUntil: now.Add(time.Minute),
	})
	if err != nil || len(claimed) != 1 || claimed[0].Kind != "active-kind" {
		t.Fatalf("старый emitter попал в разбор: %+v %v", claimed, err)
	}
	exported, err := postgres.PendingReferenceTasks(ctx, pool)
	if err != nil || len(exported) != 1 {
		t.Fatalf("экспорт: %+v %v", exported, err)
	}
	if err := postgres.ParkReferenceTasks(ctx, pool, []int64{exported[0].ID}); err != nil {
		t.Fatal(err)
	}
	var unfinished, parked bool
	var reason, revision string
	err = pool.QueryRow(ctx, pendingReferenceState, exported[0].ID).Scan(&unfinished, &parked, &reason, &revision)
	if err != nil || !unfinished || !parked || reason != "ownership-transferred" || revision != "12" {
		message := "исходная задача потеряна: unfinished=%t parked=%t reason=%s revision=%s err=%v"
		t.Fatalf(message, unfinished, parked, reason, revision, err)
	}
	remaining, err := postgres.PendingReferenceTasks(ctx, pool)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("уже экспортированное повторно выбрано: %+v %v", remaining, err)
	}
}
