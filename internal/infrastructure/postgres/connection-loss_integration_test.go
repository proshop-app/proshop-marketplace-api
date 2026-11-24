//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// terminateOtherBackends обрывает остальные сеансы тестовой базы — как перезапуск пулера
// или базы обрывает транзакцию в полёте.
const terminateOtherBackends = `SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
    WHERE datname = current_database() AND pid <> pg_backend_pid()`

func TestLostConnectionInsideTransactionIsUnavailable(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(pool)
	killer, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer killer.Release()

	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.StoreCards().Lock(ctx, "first"); err != nil {
			return err
		}
		if _, err := killer.Exec(ctx, terminateOtherBackends); err != nil {
			return err
		}
		return tx.StoreCards().Lock(ctx, "second")
	})
	if !errors.Is(err, postgres.ErrUnavailable) || errs.RetryKindOf(err) != errs.RetryTransient {
		t.Fatalf("обрыв соединения — временный сбой хранилища: %v", err)
	}
	// Повтор обычным путём получает живое соединение пула.
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.StoreCards().Lock(ctx, "retry")
	})
	if err != nil {
		t.Fatalf("повтор после обрыва: %v", err)
	}
}

func TestStatementErrorKeepsItsClass(t *testing.T) {
	pool := testPool(t)
	err := postgres.NewUnitOfWork(pool).Do(context.Background(), func(context.Context, ports.Tx) error {
		return postgres.ErrUnavailable.WithMessage("своя причина")
	})
	if errs.CodeOf(err) != errs.Unavailable {
		t.Fatal(err)
	}
	conflict := errs.New(errs.Conflict, "test.conflict", "конфликт")
	err = postgres.NewUnitOfWork(pool).Do(context.Background(), func(context.Context, ports.Tx) error {
		return conflict
	})
	if !errors.Is(err, conflict) || errs.CodeOf(err) != errs.Conflict {
		t.Fatalf("ошибка сценария со своим классом не переклассифицируется: %v", err)
	}
}
