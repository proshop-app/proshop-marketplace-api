//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
)

func TestScheduledWorkSkipsOtherReplicaForWholeInterval(t *testing.T) {
	pool := testPool(t)
	classPool, sessions := postgres.Single(pool), postgres.DirectSessionsOf(pool)
	ctx := context.Background()
	first := postgres.NewScheduledWork(classPool, sessions, "read-test", time.Hour)
	second := postgres.NewScheduledWork(classPool, sessions, "read-test", time.Hour)
	calls := 0
	run := func(context.Context) error {
		calls++
		return nil
	}
	if err := first.WithLock(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := second.WithLock(ctx, run); !errors.Is(err, ports.ErrRebuildBusy) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestScheduledWorkDoesNotOverlapLongRunAfterInterval(t *testing.T) {
	pool := testPool(t)
	classPool, sessions := postgres.Single(pool), postgres.DirectSessionsOf(pool)
	ctx := context.Background()
	first := postgres.NewScheduledWork(classPool, sessions, "long-test", time.Second)
	second := postgres.NewScheduledWork(classPool, sessions, "long-test", time.Second)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- first.WithLock(ctx, func(context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	// Интервал истёк, прежний проход ещё идёт: вторая реплика берёт интервал, но не работу.
	_, err := pool.Exec(ctx, "UPDATE job_leases SET until = '2000-01-01' WHERE name = 'long-test'")
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	err = second.WithLock(ctx, func(context.Context) error {
		t.Error("второй проход поверх незавершённого")
		return nil
	})
	close(release)
	if !errors.Is(err, ports.ErrRebuildBusy) {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Возвращённый интервал берёт следующая попытка любой реплики, не дожидаясь его истечения.
func TestScheduledWorkReleaseLetsNextAttemptRun(t *testing.T) {
	pool := testPool(t)
	classPool, sessions := postgres.Single(pool), postgres.DirectSessionsOf(pool)
	ctx := context.Background()
	first := postgres.NewScheduledWork(classPool, sessions, "release-test", 24*time.Hour)
	second := postgres.NewScheduledWork(classPool, sessions, "release-test", 24*time.Hour)
	calls := 0
	run := func(context.Context) error {
		calls++
		return nil
	}
	if err := first.WithLock(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := second.WithLock(ctx, run); err != nil || calls != 2 {
		t.Fatalf("возвращённый интервал взят следующей попыткой: calls=%d err=%v", calls, err)
	}
}

func TestShortSchedulesShareOneLease(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	clock := fake.NewClock(at(0))
	leases := postgres.NewLeases(postgres.Single(pool))
	first := jobs.NewShortSchedules(leases, "first", clock, slog.Default())
	second := jobs.NewShortSchedules(leases, "second", clock, slog.Default())
	first.Refresh(ctx)
	second.Refresh(ctx)

	calls := 0
	pass := func(context.Context) error {
		calls++
		return nil
	}
	if err := first.Run(ctx, pass); err != nil {
		t.Fatal(err)
	}
	if err := first.Run(ctx, pass); err != nil || calls != 2 {
		t.Fatalf("владелец выполняет разные расписания: %d, %v", calls, err)
	}
	if err := second.Run(ctx, pass); !errors.Is(err, ports.ErrIntervalTaken) {
		t.Fatal(err)
	}

	clock.Advance(time.Minute)
	if _, err := pool.Exec(ctx, "UPDATE job_leases SET until = '2000-01-01'"); err != nil {
		t.Fatal(err)
	}
	second.Refresh(ctx)
	if err := first.Run(ctx, pass); !errors.Is(err, ports.ErrIntervalTaken) {
		t.Fatal(err)
	}
	if err := second.Run(ctx, pass); err != nil || calls != 3 {
		t.Fatalf("следующий владелец: %d, %v", calls, err)
	}
}
