//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
)

func TestIndexRebuildHasSingleOwner(t *testing.T) {
	pool := pgtest.Pool(t)
	lock := postgres.NewIndexRebuildLock(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- lock.WithLock(ctx, func(context.Context) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	invoked := false
	err := postgres.NewIndexRebuildLock(pool).WithLock(ctx, func(context.Context) error {
		invoked = true

		return nil
	})

	if !errors.Is(err, ports.ErrRebuildBusy) || invoked {
		t.Fatalf("конкурентный владелец: invoked=%v err=%v", invoked, err)
	}
	close(release)

	if err := <-finished; err != nil {
		t.Fatal(err)
	}

	if err := lock.WithLock(ctx, func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("блокировка осталась после завершения: %v", err)
	}
}
