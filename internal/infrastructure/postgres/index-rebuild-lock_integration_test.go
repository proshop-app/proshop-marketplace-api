//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
)

const (
	selectIndexLockOwner = `
		SELECT pid FROM pg_locks
		WHERE locktype = 'advisory' AND granted
			AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`
	terminateIndexLockOwner = `SELECT pg_terminate_backend($1)`
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

func TestIndexRebuildSingleConnectionLifecycle(t *testing.T) {
	failure := errors.New("сбой пересборки")

	for _, outcome := range []string{"success", "error", "panic"} {
		t.Run(outcome, func(t *testing.T) {
			seedPool := testPool(t)
			config := seedPool.Config()
			config.MaxConns = 1
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			pool, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			uow := postgres.NewUnitOfWork(pool)
			lock := postgres.NewIndexRebuildLock(pool)

			run := func() error {
				return lock.WithLock(ctx, func(work context.Context) error {
					if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
						return fmt.Errorf("владелец блокировки занял квоту пула: %d", acquired)
					}
					if err := uow.Do(work, func(ctx context.Context, tx ports.Tx) error {
						if err := tx.Buyers().Add(ctx, buyer.New(buyerA, "70000000001", at(0))); err != nil {
							return err
						}
						fresh, err := tx.Inbox().MarkProcessed(ctx, "index-rebuild:0:1", at(0))
						if err != nil || !fresh {
							return fmt.Errorf("raw-запись вместе с ORM внутри блокировки: %v, %w", fresh, err)
						}

						return nil
					}); err != nil {
						return err
					}

					switch outcome {
					case "error":
						return failure
					case "panic":
						panic(failure)
					default:
						return nil
					}
				})
			}

			if outcome == "panic" {
				func() {
					defer func() {
						if recovered, ok := recover().(error); !ok || !errors.Is(recovered, failure) {
							t.Errorf("паника пересборки сохраняется: %v", recovered)
						}
					}()

					if err := run(); err != nil {
						t.Errorf("паника не подменяется ошибкой: %v", err)
					}
				}()
			} else {
				err := run()
				if outcome == "error" && !errors.Is(err, failure) {
					t.Fatalf("ошибка пересборки сохраняется: %v", err)
				}
				if outcome == "success" && err != nil {
					t.Fatal(err)
				}
			}

			if err := lock.WithLock(ctx, func(work context.Context) error {
				return uow.Do(work, func(ctx context.Context, tx ports.Tx) error {
					if _, err := tx.Buyers().Get(ctx, buyerA); err != nil {
						return err
					}
					fresh, err := tx.Inbox().MarkProcessed(ctx, "index-rebuild:0:1", at(1))
					if err != nil || fresh {
						return fmt.Errorf("зафиксированная raw-запись читается повторно: %v, %w", fresh, err)
					}

					return nil
				})
			}); err != nil {
				t.Fatalf("повторная блокировка после %s с MaxConns=1: %v", outcome, err)
			}
			if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
				t.Fatalf("транзакция вернула соединение в пул: %d", acquired)
			}
		})
	}
}

func TestIndexRebuildCancelsWorkWhenBackendIsLost(t *testing.T) {
	pool := testPool(t)
	lock := postgres.NewIndexRebuildLock(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := lock.WithLock(ctx, func(work context.Context) error {
		var pid int
		if err := pool.QueryRow(ctx, selectIndexLockOwner).Scan(&pid); err != nil {
			return err
		}
		var terminated bool
		if err := pool.QueryRow(ctx, terminateIndexLockOwner, pid).Scan(&terminated); err != nil {
			return err
		}
		if !terminated {
			return errors.New("соединение владельца не завершено")
		}

		select {
		case <-work.Done():
			return nil
		case <-ctx.Done():
			return fmt.Errorf("потеря владельца не отменила пересборку: %w", ctx.Err())
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("потеря соединения отменяет работу: %v", err)
	}
	if err := lock.WithLock(ctx, func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("блокировка освобождена после потери backend: %v", err)
	}
}
