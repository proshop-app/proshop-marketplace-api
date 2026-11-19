package messaging_test

import (
	"context"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

func TestRetryClassificationAndUnknownBudgetAreIndependentOfLeases(t *testing.T) {
	for _, err := range []error{
		errs.New(errs.Unavailable, "test.down", "отказ"),
		context.Canceled,
		context.DeadlineExceeded,
		errs.New(errs.Internal, "test.wrapped", "отмена").Wrap(context.Canceled),
		errs.New(errs.Conflict, "test.conflict", "версия"),
		errs.New(errs.TooManyRequests, "test.limited", "пауза"),
	} {
		result := messaging.Resolution(ports.Task{Attempts: 1000}, err, t0, 3)
		if result.FailureReason != "" || result.UnknownFailures != 0 || result.NextAt == nil {
			t.Fatal("зависимость не должна исчерпывать бюджет задачи", result)
		}
	}
	task := ports.Task{Attempts: 1000}
	for attempt := 1; attempt <= 3; attempt++ {
		result := messaging.Resolution(task, errors.New("неизвестная причина"), t0, 3)
		if (result.FailureReason != "") != (attempt == 3) || result.UnknownFailures != attempt {
			t.Fatal("неизвестные сбои считаются отдельно", result)
		}
		task.UnknownFailures = result.UnknownFailures
	}
	for _, code := range []errs.Code{errs.Invalid, errs.NotFound, errs.Forbidden, errs.Unauthorized} {
		result := messaging.Resolution(ports.Task{}, errs.New(code, "test.permanent", "отказ"), t0, 3)
		if result.FailureReason != "test.permanent" || result.NextAt != nil {
			t.Fatal("постоянный отказ повторяется", result)
		}
	}
}

func TestRequeuePreservesIntentAndFencesOldOwner(t *testing.T) {
	store, clock := fake.NewStore(), fake.NewClock(t0)
	for _, kind := range []string{"bad", "other", "bad"} {
		enqueue(t, store, kind, "t1", "", t0)
	}
	d := messaging.NewDispatcher(store, clock, config(), nil)
	for range 3 {
		if _, err := d.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	first := store.OutboxRows()[0]
	ids, err := d.Requeue(context.Background(), ports.TaskRequeue{Kind: "bad", Limit: 1})
	if err != nil || len(ids) != 1 || ids[0] != first.ID {
		t.Fatal(ids, err)
	}
	err = store.Complete(context.Background(), first.ID, first.Attempts, t0)
	if !errors.Is(err, ports.ErrTaskClaimConflict) {
		t.Fatal("возврат оживил токен старого владельца", err)
	}
	ids, err = d.Requeue(context.Background(), ports.TaskRequeue{Kind: "bad", After: ids[0], Limit: 1})
	if err != nil || len(ids) != 1 || ids[0] != 3 {
		t.Fatal(ids, err)
	}
	ids, err = d.Requeue(context.Background(), ports.TaskRequeue{ID: 2, Limit: 1})
	if err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	for _, row := range store.OutboxRows() {
		if row.FailedAt != nil || row.UnknownFailures != 0 || string(row.Payload) != "{}" {
			t.Fatal("возврат потерял намерение", row)
		}
	}
	d = messaging.NewDispatcher(store, clock, config(), map[string]messaging.Handler{
		"bad":   func(context.Context, ports.Task) error { return nil },
		"other": func(context.Context, ports.Task) error { return nil },
	})
	for range 3 {
		if _, err := d.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range store.OutboxRows() {
		if row.DoneAt == nil {
			t.Fatal("возвращённая задача не исполнена", row)
		}
	}
}

func TestShutdownLeavesClaimRecoverable(t *testing.T) {
	store, clock := fake.NewStore(), fake.NewClock(t0)
	enqueue(t, store, "work", "t1", "", t0)
	ctx, cancel := context.WithCancel(context.Background())
	d := messaging.NewDispatcher(store, clock, config(), map[string]messaging.Handler{
		"work": func(context.Context, ports.Task) error {
			cancel()
			return messaging.ErrInvalidTask
		},
	})
	if _, err := d.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	row := store.OutboxRows()[0]
	if row.FailedAt != nil || row.DoneAt != nil {
		t.Fatal("остановка стала итогом задачи", row)
	}
	clock.Advance(config().Lease)
	d = messaging.NewDispatcher(store, clock, config(), map[string]messaging.Handler{
		"work": func(context.Context, ports.Task) error { return nil },
	})
	if _, err := d.RunOnce(context.Background()); err != nil || store.OutboxRows()[0].DoneAt == nil {
		t.Fatal("новый процесс не восстановил аренду", err)
	}
}
