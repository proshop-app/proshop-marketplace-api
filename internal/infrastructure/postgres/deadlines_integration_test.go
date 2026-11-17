//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func TestDatabaseOperationsHaveFiniteBudgets(t *testing.T) {
	pool := testPool(t)
	var statement, idle string
	err := pool.QueryRow(context.Background(), `SELECT current_setting('statement_timeout'),
        current_setting('idle_in_transaction_session_timeout')`).Scan(&statement, &idle)
	if err != nil || statement == "0" || idle == "0" {
		t.Fatalf("серверные сроки не заданы: statement=%s idle=%s err=%v", statement, idle, err)
	}
	err = postgres.NewUnitOfWork(pool).Do(context.Background(), func(ctx context.Context, _ ports.Tx) error {
		if _, ok := ctx.Deadline(); !ok {
			return fmt.Errorf("у транзакции нет общего срока")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseClassTimeoutAndPoolStateDoNotLeak(t *testing.T) {
	database := testPool(t)
	ctx := context.Background()
	pool, err := postgres.Open(ctx, database.Config().ConnString(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	budget := ports.DatabaseBudget{Statement: 75 * time.Millisecond, Transaction: time.Second}
	limited := ports.WithDatabaseBudget(ctx, budget)
	for _, attempt := range []struct {
		ctx      context.Context
		expected string
	}{
		{ctx: limited, expected: "75ms"},
		{ctx: ctx, expected: "10s"},
		{ctx: limited, expected: "75ms"},
	} {
		var statement string
		err := pool.QueryRow(attempt.ctx, `SHOW statement_timeout`).Scan(&statement)
		if err != nil || statement != attempt.expected {
			t.Fatalf("настройки предыдущего заёмщика не протекают: %s, %v", statement, err)
		}
	}
	blocker, err := database.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('blocked', 38))`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = blocker.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended('blocked', 38))`)
	}()
	started := time.Now()
	err = postgres.NewUnitOfWork(pool).Do(limited, func(ctx context.Context, tx ports.Tx) error {
		return tx.StoreCards().Lock(ctx, "blocked")
	})
	var failure *pgconn.PgError
	if !errors.As(err, &failure) || failure.Code != "57014" || time.Since(started) > time.Second {
		t.Fatal("сервер отменяет зависший SQL по классу операции", err, time.Since(started))
	}
	// Ожидание занятого пула входит в общий срок операции, а не начинается после Acquire.
	held, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	short := ports.WithDatabaseBudget(ctx, ports.DatabaseBudget{
		Statement:   50 * time.Millisecond,
		Transaction: 100 * time.Millisecond,
	})
	started = time.Now()
	err = postgres.NewUnitOfWork(pool).Do(short, func(context.Context, ports.Tx) error {
		t.Error("транзакция не должна получить занятое соединение")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatal("ожидание пула ограничено", err, time.Since(started))
	}
}

func TestClientCancellationInterruptsSQLAndReleasesConnection(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('blocked', 38))`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = blocker.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended('blocked', 38))`)
	}()
	for _, deadline := range []bool{false, true} {
		operation, cancel := context.WithCancel(ctx)
		if deadline {
			cancel()
			operation, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
		}
		started := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- postgres.NewUnitOfWork(pool).Do(operation, func(ctx context.Context, tx ports.Tx) error {
				close(started)
				return tx.StoreCards().Lock(ctx, "blocked")
			})
		}()
		<-started
		if !deadline {
			cancel()
		}
		select {
		case err := <-done:
			if err == nil {
				t.Error("отменённая операция завершилась успешно")
			}
		case <-time.After(time.Second):
			t.Error("отмена не прервала SQL")
		}
		cancel()
	}
	var waiting int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
        WHERE datname = current_database() AND wait_event = 'advisory'`).Scan(&waiting)
	if err != nil || waiting != 0 {
		t.Fatal("отменённый SQL продолжает занимать сервер", waiting, err)
	}
}
