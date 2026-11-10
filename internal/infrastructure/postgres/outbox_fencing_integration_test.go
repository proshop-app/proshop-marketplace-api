//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const selectOutboxClaimState = `SELECT attempts, next_at, done_at, last_error FROM outbox WHERE id = $1`

type outboxClaimState struct {
	attempts  int
	nextAt    time.Time
	doneAt    *time.Time
	lastError *string
}

func readOutboxClaimState(t *testing.T, pool *pgxpool.Pool, id int64) outboxClaimState {
	t.Helper()

	var state outboxClaimState
	err := pool.QueryRow(context.Background(), selectOutboxClaimState, id).Scan(
		&state.attempts,
		&state.nextAt,
		&state.doneAt,
		&state.lastError,
	)
	if err != nil {
		t.Fatal(err)
	}

	return state
}

func TestOutboxOnlyCurrentClaimCanUpdate(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	queue := postgres.NewTaskQueue(pool)
	ctx := context.Background()
	enqueue(t, uow, "t1", "k1", 0)

	first, err := queue.Claim(ctx, ports.ClaimRequest{Now: at(1), Limit: 1, PerTenant: 1, LeaseUntil: at(2)})
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	second, err := queue.Claim(ctx, ports.ClaimRequest{Now: at(3), Limit: 1, PerTenant: 1, LeaseUntil: at(4)})
	if err != nil || len(second) != 1 || second[0].Attempts != first[0].Attempts+1 {
		t.Fatal(second, err)
	}

	before := readOutboxClaimState(t, pool, first[0].ID)
	for name, update := range map[string]func() error{
		"complete": func() error {
			return queue.Complete(ctx, first[0].ID, first[0].Attempts, at(3))
		},
		"retry": func() error {
			return queue.Retry(ctx, first[0].ID, first[0].Attempts, at(30), "устаревшая ошибка")
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := update(); !errors.Is(err, ports.ErrTaskClaimConflict) || errs.CodeOf(err) != errs.Conflict {
				t.Fatalf("устаревший владелец должен получить конфликт: %v", err)
			}
			if after := readOutboxClaimState(t, pool, first[0].ID); !reflect.DeepEqual(before, after) {
				t.Fatalf("устаревший владелец изменил задачу: до %+v, после %+v", before, after)
			}
		})
	}

	if err := queue.Retry(ctx, second[0].ID, second[0].Attempts, at(6), "повторная ошибка"); err != nil {
		t.Fatal(err)
	}
	third, err := queue.Claim(ctx, ports.ClaimRequest{Now: at(6), Limit: 1, PerTenant: 1, LeaseUntil: at(7)})
	if err != nil || len(third) != 1 || third[0].Attempts != second[0].Attempts+1 {
		t.Fatal(third, err)
	}
	if err := queue.Complete(ctx, third[0].ID, third[0].Attempts, at(6)); err != nil {
		t.Fatal(err)
	}
	completed := readOutboxClaimState(t, pool, third[0].ID)
	if completed.doneAt == nil || !completed.doneAt.Equal(at(6)) || completed.lastError != nil {
		t.Fatalf("текущий владелец завершает задачу и очищает ошибку: %+v", completed)
	}
	if err := queue.Complete(ctx, third[0].ID, third[0].Attempts, at(8)); !errors.Is(err, ports.ErrTaskClaimConflict) {
		t.Fatalf("повторное завершение должно вернуть конфликт: %v", err)
	}
	err = queue.Retry(ctx, third[0].ID, third[0].Attempts, at(30), "поздняя ошибка")
	if !errors.Is(err, ports.ErrTaskClaimConflict) {
		t.Fatalf("завершённую задачу нельзя отложить: %v", err)
	}
	if after := readOutboxClaimState(t, pool, third[0].ID); !reflect.DeepEqual(completed, after) {
		t.Fatalf("завершённая задача изменилась: до %+v, после %+v", completed, after)
	}
}

func TestOutboxRejectsMissingAndUnclaimedTask(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	queue := postgres.NewTaskQueue(pool)
	ctx := context.Background()

	if err := queue.Complete(ctx, 1, 1, at(1)); !errors.Is(err, ports.ErrTaskClaimConflict) {
		t.Fatalf("отсутствующую задачу нельзя завершить: %v", err)
	}
	if err := queue.Retry(ctx, 1, 1, at(1), "ошибка"); !errors.Is(err, ports.ErrTaskClaimConflict) {
		t.Fatalf("отсутствующую задачу нельзя отложить: %v", err)
	}

	enqueue(t, uow, "t1", "k1", 0)
	if err := queue.Complete(ctx, 1, 0, at(1)); !errors.Is(err, ports.ErrTaskClaimConflict) {
		t.Fatalf("невзятую задачу нельзя завершить: %v", err)
	}
	if err := queue.Retry(ctx, 1, 0, at(1), "ошибка"); !errors.Is(err, ports.ErrTaskClaimConflict) {
		t.Fatalf("невзятую задачу нельзя отложить: %v", err)
	}
	state := readOutboxClaimState(t, pool, 1)
	if state.attempts != 0 || state.doneAt != nil || state.lastError != nil || !state.nextAt.Equal(at(0)) {
		t.Fatalf("невзятая задача изменилась: %+v", state)
	}
}
