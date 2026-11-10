//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const (
	lockReferenceBatchEntry  = `SELECT id FROM reference.entries WHERE id = $1 FOR UPDATE`
	selectReferenceBatchWait = `
		SELECT EXISTS (
			SELECT 1
			FROM pg_stat_activity
			WHERE datname = current_database() AND application_name = $1 AND wait_event_type = 'Lock'
		)`
	selectReferenceBatchInbox = `SELECT count(*) FROM processed_events WHERE id = $1`
	referenceBatchApplication = "reference_gorm_cancel"
	referenceBatchEvent       = "reference-cancel:0:1"
)

var errReferenceBatchTrace = errors.New("паника трассировщика native-пачки")

type referenceBatchPanicTrace struct {
	panicked atomic.Bool
}

type referenceBatchPanicResult struct {
	recovered any
	err       error
}

// Отмена приходит при активной native-пачке: database/sql одновременно пытается
// откатить *sql.Tx. Пачка и автоматический rollback должны пользоваться соединением последовательно.
func TestReferenceGORMCancelDuringBatchRollsBackAndReleasesConnection(t *testing.T) {
	service, observer, _ := referenceService(t)
	created := createReference(t, service, "Запись", reference.StatusCandidate)
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()

	config := observer.Config()
	config.MaxConns = 1
	config.ConnConfig.RuntimeParams["application_name"] = referenceBatchApplication
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	uow := postgres.NewUnitOfWork(pool)

	var entry *reference.Entry
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		entry, err = tx.Reference().Entry(ctx, referenceDict, created.Entry.ID)

		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	entry.Meet(time.Now())

	blocker, err := observer.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = blocker.Rollback(context.Background())
	}()

	var locked string
	if err := blocker.QueryRow(ctx, lockReferenceBatchEntry, string(entry.ID())).Scan(&locked); err != nil {
		t.Fatal(err)
	}

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var observed atomic.Bool
	finished := make(chan error, 1)
	go func() {
		finished <- uow.Do(workerCtx, func(ctx context.Context, tx ports.Tx) error {
			tx.AfterCommit(func() {
				observed.Store(true)
			})
			if _, err := tx.Inbox().MarkProcessed(ctx, referenceBatchEvent, time.Now()); err != nil {
				return err
			}

			return tx.Reference().Save(ctx, []*reference.Entry{entry}, nil)
		})
	}()

	// Ждём реальную блокировку на сервере, а не предполагаем, что горутина уже начала пачку.
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	waiting := false
	for !waiting {
		err := observer.QueryRow(ctx, selectReferenceBatchWait, referenceBatchApplication).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}

		select {
		case err := <-finished:
			t.Fatalf("пачка завершилась до блокировки: %v", err)
		case <-ctx.Done():
			t.Fatal("пачка не дошла до заблокированной строки")
		case <-poll.C:
		}
	}

	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("отмена пачки сохраняет причину: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("отмена пачки зависла на совместном соединении GORM и pgx")
	}

	if observed.Load() {
		t.Fatal("AfterCommit вызван после отмены пачки")
	}
	if entry.Origin().Version != created.Entry.Version {
		t.Fatal("отменённая пачка отметила агрегат сохранённым")
	}
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// Пул из одного соединения должен обслужить следующую транзакцию; запись
	// агрегата и отметка inbox предыдущего сценария должны быть целиком отменены.
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		stored, err := tx.Reference().Entry(ctx, referenceDict, created.Entry.ID)
		if err != nil {
			return err
		}
		state := stored.State()
		if state.Version != created.Entry.Version || state.Occurrences != created.Entry.Occurrences {
			t.Errorf("отменённая пачка изменила запись: %+v", state)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("следующая транзакция на том же пуле: %v", err)
	}
	if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("пачка и следующая транзакция удерживают соединения: %d", acquired)
	}
	var processed int
	if err := observer.QueryRow(ctx, selectReferenceBatchInbox, referenceBatchEvent).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if processed != 0 {
		t.Fatal("отмена native-пачки оставила отметку inbox")
	}
}

// Panic внутри Raw нельзя пропускать в database/sql: его попытка закрыть
// sql.Conn ждала бы незавершённую sql.Tx, а UnitOfWork ещё не мог бы начать rollback.
func TestReferenceGORMBatchPanicRollsBackWithoutDeadlock(t *testing.T) {
	service, observer, _ := referenceService(t)
	created := createReference(t, service, "Запись", reference.StatusCandidate)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	trace := &referenceBatchPanicTrace{}
	config := observer.Config()
	config.MaxConns = 1
	config.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closed := make(chan struct{})
		go func() {
			pool.Close()
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Error("пул удерживает соединение после паники пачки")
		}
	})
	uow := postgres.NewUnitOfWork(pool)

	var entry *reference.Entry
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		entry, err = tx.Reference().Entry(ctx, referenceDict, created.Entry.ID)

		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	entry.Meet(time.Now())

	var observed atomic.Bool
	finished := make(chan referenceBatchPanicResult, 1)
	go func() {
		result := referenceBatchPanicResult{}
		defer func() {
			result.recovered = recover()
			finished <- result
		}()
		result.err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			tx.AfterCommit(func() {
				observed.Store(true)
			})
			if _, err := tx.Inbox().MarkProcessed(ctx, referenceBatchEvent, time.Now()); err != nil {
				return err
			}

			return tx.Reference().Save(ctx, []*reference.Entry{entry}, nil)
		})
	}()

	select {
	case result := <-finished:
		recovered, ok := result.recovered.(error)
		if !ok || !errors.Is(recovered, errReferenceBatchTrace) || result.err != nil {
			t.Fatalf("паника пачки не сохранилась: recovered=%v, err=%v", result.recovered, result.err)
		}
	case <-ctx.Done():
		t.Fatal("паника внутри sql.Conn.Raw заблокировала rollback транзакции")
	}

	if observed.Load() {
		t.Fatal("AfterCommit вызван после паники пачки")
	}
	if entry.Origin().Version != created.Entry.Version {
		t.Fatal("пачка с паникой отметила агрегат сохранённым")
	}
	var processed int
	if err := observer.QueryRow(ctx, selectReferenceBatchInbox, referenceBatchEvent).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if processed != 0 {
		t.Fatal("паника пачки оставила отметку inbox")
	}

	// Повторяем ту же пачку на пуле с одним соединением: первая транзакция
	// должна откатиться, а паника трассировщика не должна отравить следующую.
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		stored, err := tx.Reference().Entry(ctx, referenceDict, created.Entry.ID)
		if err != nil {
			return err
		}
		if state := stored.State(); state.Version != created.Entry.Version {
			t.Errorf("пачка с паникой изменила версию записи: %d", state.Version)
		}

		return tx.Reference().Save(ctx, []*reference.Entry{entry}, nil)
	})
	if err != nil {
		t.Fatalf("повторная пачка после паники: %v", err)
	}
	if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("пачка с паникой удерживает соединения: %d", acquired)
	}
}

func (*referenceBatchPanicTrace) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	_ pgx.TraceQueryStartData,
) context.Context {
	return ctx
}

func (*referenceBatchPanicTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (trace *referenceBatchPanicTrace) TraceBatchStart(
	ctx context.Context,
	_ *pgx.Conn,
	_ pgx.TraceBatchStartData,
) context.Context {
	if trace.panicked.CompareAndSwap(false, true) {
		panic(errReferenceBatchTrace)
	}

	return ctx
}

func (*referenceBatchPanicTrace) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {
}

func (*referenceBatchPanicTrace) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}
