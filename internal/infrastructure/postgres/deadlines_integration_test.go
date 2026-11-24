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

func TestPoolsApplyClassBudgetsAndQuotas(t *testing.T) {
	database := testPool(t)
	ctx := context.Background()
	fast := postgres.Budget{Statement: 75 * time.Millisecond, Transaction: 300 * time.Millisecond}
	slow := postgres.Budget{Statement: 10 * time.Second, Transaction: 30 * time.Second}
	target := postgres.Target{URL: database.Config().ConnString()}
	pools, err := postgres.OpenPools(ctx, target, map[ports.DatabaseClass]postgres.PoolConfig{
		ports.DatabaseRead:       {MaxConns: 1, Budget: fast},
		ports.DatabaseCheckout:   {MaxConns: 1, Budget: slow},
		ports.DatabaseIngestion:  {MaxConns: 1, Budget: slow},
		ports.DatabaseBackground: {MaxConns: 1, Budget: slow},
		ports.DatabaseOrders:     {MaxConns: 1, Budget: slow},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pools.Close()
	for class, expected := range map[ports.DatabaseClass]string{
		ports.DatabaseRead:       "75ms",
		ports.DatabaseBackground: "10s",
	} {
		var statement string
		err := pools.Pool(class).QueryRow(ctx, `SHOW statement_timeout`).Scan(&statement)
		if err != nil || statement != expected {
			t.Fatalf("сроки класса заданы пулу при соединении: %s, %v", statement, err)
		}
	}
	read := ports.WithDatabaseClass(ctx, ports.DatabaseRead)
	uow := postgres.NewUnitOfWorkByClass(pools)

	// Квота приёма занята целиком — чтение покупателя получает своё соединение.
	ingestion, err := pools.Pool(ports.DatabaseIngestion).Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer ingestion.Release()
	if err := uow.Do(read, func(context.Context, ports.Tx) error { return nil }); err != nil {
		t.Fatal("приём не занимает соединения покупателей", err)
	}
	// Фон (outbox индексации, циклы) занят целиком — исполнитель оформления не ждёт.
	background, err := pools.Pool(ports.DatabaseBackground).Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer background.Release()
	orders := ports.WithDatabaseClass(ctx, ports.DatabaseOrders)
	if err := uow.Do(orders, func(context.Context, ports.Tx) error { return nil }); err != nil {
		t.Fatal("фон не занимает соединения исполнителя оформления", err)
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
	err = uow.Do(read, func(ctx context.Context, tx ports.Tx) error {
		return tx.StoreCards().Lock(ctx, "blocked")
	})
	var failure *pgconn.PgError
	if !errors.As(err, &failure) || failure.Code != "57014" || time.Since(started) > time.Second {
		t.Fatal("сервер отменяет зависший SQL по сроку класса", err, time.Since(started))
	}
	// Ожидание занятого пула входит в общий срок операции, а не начинается после Acquire.
	held, err := pools.Pool(ports.DatabaseRead).Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	started = time.Now()
	err = uow.Do(read, func(context.Context, ports.Tx) error {
		t.Error("транзакция не должна получить занятое соединение")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatal("ожидание пула ограничено сроком класса", err, time.Since(started))
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
