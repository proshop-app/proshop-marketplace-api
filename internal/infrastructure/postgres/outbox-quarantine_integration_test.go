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
	queue := postgres.NewTaskQueue(postgres.Single(pool))
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
	retention := postgres.NewRetention(postgres.Single(pool))
	_, err = retention.Purge(ctx, ports.PurgeRequest{PurchaseQuarantineBefore: at(50), Batch: 10})
	if err != nil {
		t.Fatal(err)
	}
	failures, err := postgres.NewTaskQueue(postgres.Single(pool)).Failures(ctx, 0, 10)
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

func TestQuarantineReturnPreservesIntentAndUnknownBudget(t *testing.T) {
	pool := testPool(t)
	uow, queue := postgres.NewUnitOfWork(pool), postgres.NewTaskQueue(postgres.Single(pool))
	ctx := context.Background()
	for _, key := range []string{"one", "two", "three"} {
		enqueue(t, uow, "t1", key, 0)
	}
	request := ports.ClaimRequest{Now: at(1), Limit: 3, PerTenant: 3, LeaseUntil: at(2)}
	tasks, err := queue.Claim(ctx, request)
	if err != nil || len(tasks) != 3 {
		t.Fatal(tasks, err)
	}
	for _, task := range tasks {
		result := ports.TaskResolution{
			ID:              task.ID,
			Attempts:        task.Attempts,
			At:              at(1),
			UnknownFailures: 3,
			FailureReason:   "attempts_exhausted",
			Error:           "диагностика",
		}
		if err := queue.Resolve(ctx, []ports.TaskResolution{result}); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := queue.Requeue(ctx, ports.TaskRequeue{Kind: "test", Limit: 1, At: at(2)})
	if err != nil || len(ids) != 1 || ids[0] != tasks[0].ID {
		t.Fatal(ids, err)
	}
	if err := queue.Complete(ctx, tasks[0].ID, tasks[0].Attempts, at(3)); !errors.Is(err, ports.ErrTaskClaimConflict) {
		t.Fatal("старый владелец ожил", err)
	}
	ids, err = queue.Requeue(ctx, ports.TaskRequeue{Kind: "test", After: ids[0], Limit: 1, At: at(2)})
	if err != nil || len(ids) != 1 || ids[0] != tasks[1].ID {
		t.Fatal(ids, err)
	}
	ids, err = queue.Requeue(ctx, ports.TaskRequeue{ID: tasks[2].ID, Limit: 1, At: at(2)})
	if err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	request.Now = at(3)
	tasks, err = queue.Claim(ctx, request)
	if err != nil || len(tasks) != 3 {
		t.Fatal(tasks, err)
	}
	for _, task := range tasks {
		if task.UnknownFailures != 0 || len(task.Payload) == 0 || task.Attempts != 3 {
			t.Fatal("возврат не сохранил намерение или токен", task)
		}
		result := ports.TaskResolution{
			ID:              task.ID,
			Attempts:        task.Attempts,
			At:              at(3),
			UnknownFailures: 1,
			NextAt:          &request.Now,
		}
		if err := queue.Resolve(ctx, []ports.TaskResolution{result}); err != nil {
			t.Fatal(err)
		}
	}
	request.Now = at(4)
	tasks, err = queue.Claim(ctx, request)
	if err != nil || len(tasks) != 3 {
		t.Fatal(tasks, err)
	}
	for _, task := range tasks {
		if task.UnknownFailures != 1 {
			t.Fatal("счётчик не сохранился", task)
		}
		if err := queue.Complete(ctx, task.ID, task.Attempts, at(4)); err != nil {
			t.Fatal(err)
		}
	}
	failures, err := queue.Failures(ctx, 0, 100)
	if err != nil || len(failures) != 0 {
		t.Fatal(failures, err)
	}
}
