//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func TestProjectionValidationLocksBothHeads(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	locked, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
			if _, err := tx.ProjectionValidation().Current(ctx, "t1", "p1"); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	consumed := false
	defer func() {
		close(release)
		if !consumed {
			if err := <-finished; err != nil {
				t.Error(err)
			}
		}
	}()
	select {
	case <-locked:
	case err := <-finished:
		consumed = true
		t.Fatal(err)
	}
	checks := []func(context.Context, ports.Tx) error{
		func(ctx context.Context, tx ports.Tx) error { return tx.Taxonomy().Lock(ctx) },
		func(ctx context.Context, tx ports.Tx) error {
			return tx.ModerationRules().AddPhrase(ctx, moderation.StopPhrase{Key: "phrase", Text: "phrase"})
		},
	}
	for _, check := range checks {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := uow.Do(ctx, check)
		cancel()
		if err == nil {
			t.Fatal("писатель обошёл FOR SHARE проверки версий")
		}
	}
}
