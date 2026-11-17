//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func TestOutboxQuarantineIsFencedDurableAndExcluded(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	queue := postgres.NewTaskQueue(pool)
	ctx := context.Background()
	enqueue(t, uow, "t1", "bad", 0)
	tasks, err := queue.Claim(ctx, ports.ClaimRequest{Now: at(1), Limit: 1, PerTenant: 1, LeaseUntil: at(2)})
	if err != nil || len(tasks) != 1 {
		t.Fatal(tasks, err)
	}
	failure := ports.TaskResolution{
		ID:            tasks[0].ID,
		Attempts:      tasks[0].Attempts,
		At:            at(1),
		FailureReason: "test.invalid",
		Error:         "диагностика",
	}
	if err := queue.Resolve(ctx, []ports.TaskResolution{failure}); err != nil {
		t.Fatal(err)
	}
	err = queue.Retry(ctx, failure.ID, failure.Attempts, at(2), "поздний повтор")
	if !errors.Is(err, ports.ErrTaskClaimConflict) {
		t.Fatal("карантин защищён от устаревшего владельца", err)
	}
	if err := queue.Complete(ctx, failure.ID, failure.Attempts, at(2)); !errors.Is(err, ports.ErrTaskClaimConflict) {
		t.Fatal("карантин нельзя превратить в выполненную задачу", err)
	}
	_, err = postgres.NewRetention(pool).Purge(ctx, ports.PurgeRequest{OutboxDoneBefore: at(50), Batch: 10})
	if err != nil {
		t.Fatal(err)
	}
	failures, err := postgres.NewTaskQueue(pool).Failures(ctx, 0, 10)
	if err != nil || len(failures) != 1 || failures[0].Reason != "test.invalid" {
		t.Fatal(failures, err)
	}
	var payload, diagnostic string
	err = pool.QueryRow(ctx, `SELECT payload::text, last_error FROM outbox WHERE id = $1`, failure.ID).
		Scan(&payload, &diagnostic)
	if err != nil || payload == "" || diagnostic != failure.Error {
		t.Fatal("исходная задача и причина сохраняются", payload, diagnostic, err)
	}
	enqueue(t, uow, "t1", "good", 3)
	tasks, err = queue.Claim(ctx, ports.ClaimRequest{Now: at(5), Limit: 1, PerTenant: 1, LeaseUntil: at(6)})
	if err != nil || len(tasks) != 1 || tasks[0].ID == failure.ID {
		t.Fatal("карантин не держит квоту магазина", tasks, err)
	}
}
