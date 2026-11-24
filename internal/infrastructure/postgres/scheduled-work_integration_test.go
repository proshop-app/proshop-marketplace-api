//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func TestScheduledWorkSkipsOtherReplicaForWholeInterval(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	first := postgres.NewScheduledWork(pool, "read-test", time.Hour)
	second := postgres.NewScheduledWork(pool, "read-test", time.Hour)
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
	ctx := context.Background()
	first := postgres.NewScheduledWork(pool, "long-test", time.Second)
	second := postgres.NewScheduledWork(pool, "long-test", time.Second)
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
	time.Sleep(1100 * time.Millisecond)
	err := second.WithLock(ctx, func(context.Context) error {
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
