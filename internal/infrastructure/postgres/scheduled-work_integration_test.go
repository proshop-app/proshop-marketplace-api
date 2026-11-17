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
