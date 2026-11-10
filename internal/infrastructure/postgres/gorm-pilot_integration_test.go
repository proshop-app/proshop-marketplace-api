//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	selectGORMPilotWrites = `
		SELECT
			(SELECT count(*) FROM buyers),
			(SELECT count(*) FROM phone_challenges),
			(SELECT count(*) FROM processed_events),
			(SELECT count(*) FROM outbox),
			(SELECT count(DISTINCT xid) FROM (
				SELECT xmin::text AS xid FROM buyers
				UNION ALL SELECT xmin::text FROM phone_challenges
				UNION ALL SELECT xmin::text FROM processed_events
				UNION ALL SELECT xmin::text FROM outbox
			) written)`

	selectGORMPilotChallengeCounters = `
		SELECT checks, last_checked_at IS NULL
		FROM phone_challenges
		WHERE id = $1`

	selectGORMPilotScopeWrites = `
		SELECT
			(SELECT count(*) FROM processed_events),
			(SELECT count(*) FROM outbox),
			(SELECT count(DISTINCT xid) FROM (
				SELECT xmin::text AS xid FROM phone_challenges WHERE id = $1
				UNION ALL SELECT xmin::text FROM processed_events
				UNION ALL SELECT xmin::text FROM outbox
			) written)`
)

type gormPilotWrites struct {
	buyers     int
	challenges int
	processed  int
	outbox     int
	xids       int
}

type gormPilotChallengeResult struct {
	status buyer.ChallengeStatus
	err    error
}

type gormPilotScopeInput struct {
	id          ids.UUID
	phone       buyer.Phone
	providerRef string
	rollback    bool
}

type gormPilotQueryTrace struct {
	mu         sync.Mutex
	operations []string
}

func TestGORMPilotSharesNativeTransaction(t *testing.T) {
	failure := errors.New("сбой сценария")

	for _, outcome := range []string{"commit", "rollback", "panic"} {
		t.Run(outcome, func(t *testing.T) {
			pool := testPool(t)
			uow := postgres.NewUnitOfWork(pool)
			ctx := context.Background()
			observations := 0
			var observationError error

			run := func() error {
				return uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
					tx.AfterCommit(func() {
						observations++
						observationError = gormPilotExpectWrites(ctx, pool, 1)
					})

					if err := gormPilotWrite(ctx, tx); err != nil {
						return err
					}
					if err := gormPilotExpectWrites(ctx, pool, 0); err != nil {
						return fmt.Errorf("записи не видны другому соединению до commit: %w", err)
					}

					switch outcome {
					case "rollback":
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
							t.Errorf("паника сценария сохраняется: %v", recovered)
						}
					}()

					if err := run(); err != nil {
						t.Errorf("паника не подменяется ошибкой: %v", err)
					}
				}()
			} else {
				err := run()
				if outcome == "rollback" && !errors.Is(err, failure) {
					t.Fatalf("ошибка сценария сохраняется: %v", err)
				}
				if outcome == "commit" && err != nil {
					t.Fatal(err)
				}
			}

			want := 0
			if outcome == "commit" {
				want = 1
			}
			if err := gormPilotExpectWrites(ctx, pool, want); err != nil {
				t.Fatal(err)
			}
			if observations != want || observationError != nil {
				t.Fatalf("AfterCommit видит только зафиксированные записи: %d, %v", observations, observationError)
			}
		})
	}
}

func TestGORMPilotSwallowedErrorRollsBackNativeWrites(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	observed := false
	var duplicateError error

	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tx.AfterCommit(func() {
			observed = true
		})

		if err := gormPilotWrite(ctx, tx); err != nil {
			return err
		}

		duplicateError = tx.Buyers().Add(ctx, buyer.New(buyerB, "70000000001", at(0)))

		return nil
	})
	if !errors.Is(duplicateError, buyer.ErrExists) {
		t.Fatalf("уникальный телефон сохраняет доменную ошибку: %v", duplicateError)
	}
	if err == nil || errors.Is(err, postgres.ErrUnavailable) {
		t.Fatalf("ошибка GORM прерывает общую транзакцию и не означает недоступность базы: %v", err)
	}
	if observed {
		t.Fatal("AfterCommit не вызывается после отказа commit")
	}
	if err := gormPilotExpectWrites(ctx, pool, 0); err != nil {
		t.Fatal(err)
	}
}

func TestGORMPilotSingleConnectionLifecycle(t *testing.T) {
	failure := errors.New("сбой сценария")

	for _, outcome := range []string{"commit", "rollback", "panic", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			observerPool := testPool(t)
			config := observerPool.Config()
			config.MaxConns = 1
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			pool, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			uow := postgres.NewUnitOfWork(pool)
			observed := false
			var observationError error

			run := func() error {
				return uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
					tx.AfterCommit(func() {
						observed = true
						observationError = uow.Do(ctx, func(ctx context.Context, next ports.Tx) error {
							if _, err := next.Buyers().Get(ctx, buyerA); err != nil {
								return err
							}
							if fresh, err := next.Inbox().MarkProcessed(ctx, "topic:0:1", at(1)); err != nil || fresh {
								return fmt.Errorf("повторная отметка после commit: %v, %w", fresh, err)
							}

							return nil
						})
					})

					if err := gormPilotWrite(ctx, tx); err != nil {
						return err
					}

					switch outcome {
					case "rollback":
						return failure
					case "panic":
						panic(failure)
					case "cancel":
						cancel()

						return ctx.Err()
					default:
						return nil
					}
				})
			}

			if outcome == "panic" {
				func() {
					defer func() {
						if recovered, ok := recover().(error); !ok || !errors.Is(recovered, failure) {
							t.Errorf("паника сценария сохраняется: %v", recovered)
						}
					}()

					if err := run(); err != nil {
						t.Errorf("паника не подменяется ошибкой: %v", err)
					}
				}()
			} else {
				err := run()
				switch outcome {
				case "commit":
					if err != nil {
						t.Fatal(err)
					}
				case "rollback":
					if !errors.Is(err, failure) {
						t.Fatalf("ошибка сценария сохраняется: %v", err)
					}
				case "cancel":
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("отмена сценария сохраняется: %v", err)
					}
				}
			}

			if outcome == "cancel" {
				// Отменённый sql.Tx закрывает соединение, а pgxpool удаляет его асинхронно.
				// Следующая транзакция доказывает освобождение единственного места в пуле.
				verificationCtx, verificationCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer verificationCancel()
				if err := uow.Do(verificationCtx, func(context.Context, ports.Tx) error {
					return nil
				}); err != nil {
					t.Fatalf("после отмены доступна следующая транзакция при MaxConns=1: %v", err)
				}
			}

			if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
				t.Fatalf("соединение возвращается в пул после %s: %d", outcome, acquired)
			}
			committed := outcome == "commit"
			if observed != committed || observationError != nil {
				t.Fatalf("AfterCommit открывает транзакцию при MaxConns=1: %v, %v", observed, observationError)
			}
			want := 0
			if committed {
				want = 1
			}
			if err := gormPilotExpectWrites(context.Background(), observerPool, want); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGORMPilotChallengeConcurrentCAS(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	challenge := gormPilotChallenge()
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Challenges().Add(ctx, challenge)
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ready := make(chan error, 2)
	start := make(chan struct{})
	results := make(chan gormPilotChallengeResult, 2)

	for _, status := range []buyer.CallStatus{buyer.CallConfirmed, buyer.CallExpired} {
		go func() {
			result := gormPilotChallengeResult{}
			result.err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				loaded, err := tx.Challenges().Get(ctx, buyerB)
				ready <- err
				if err != nil {
					return err
				}

				select {
				case <-start:
				case <-ctx.Done():
					return ctx.Err()
				}

				loaded.Observe(status, at(1))
				result.status = loaded.Status()

				return tx.Challenges().Update(ctx, loaded)
			})
			results <- result
		}()
	}

	for range 2 {
		select {
		case err := <-ready:
			if err != nil {
				t.Error(err)
			}
		case <-ctx.Done():
			t.Fatal("обе транзакции должны прочитать исходную версию", ctx.Err())
		}
	}
	close(start)

	var (
		winner    buyer.ChallengeStatus
		succeeded int
		conflicts int
	)
	for range 2 {
		var result gormPilotChallengeResult

		select {
		case result = <-results:
		case <-ctx.Done():
			t.Fatal("конкурирующие транзакции должны завершиться", ctx.Err())
		}

		switch {
		case result.err == nil:
			winner = result.status
			succeeded++
		case errors.Is(result.err, buyer.ErrChallengeConflict):
			conflicts++
		default:
			t.Errorf("конкурирующее обновление: %v", result.err)
		}
	}
	if succeeded != 1 || conflicts != 1 {
		t.Fatalf("одна версия сохраняется ровно один раз: успехов %d, конфликтов %d", succeeded, conflicts)
	}

	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		loaded, err := tx.Challenges().Get(ctx, buyerB)
		if err != nil {
			return err
		}
		state := loaded.State()
		if state.Status != winner || state.Version != 2 || state.Checks != 1 {
			return fmt.Errorf("сохранён только победивший переход: %+v", state)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGORMPilotColdConcurrentScopes(t *testing.T) {
	pool := testPool(t)
	seed := postgres.NewUnitOfWork(pool)
	inputs := []gormPilotScopeInput{
		{
			id:          buyerA,
			phone:       "70000000001",
			providerRef: "provider-1",
			rollback:    true,
		},
		{
			id:          buyerB,
			phone:       "70000000002",
			providerRef: "provider-2",
		},
	}
	if err := do(t, seed, func(ctx context.Context, tx ports.Tx) error {
		for _, input := range inputs {
			if err := tx.Buyers().Add(ctx, buyer.New(input.id, input.phone, at(0))); err != nil {
				return err
			}
			challenge := buyer.NewChallenge(
				input.id,
				input.phone,
				buyer.CallInstruction{ProviderRef: input.providerRef},
				at(0),
				5*time.Minute,
			)
			if err := tx.Challenges().Add(ctx, challenge); err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Первый Do на новом UOW вызывается одновременно: шаблон ORM ещё не прогрет seed-транзакцией.
	uow := postgres.NewUnitOfWork(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	entered := make(chan struct{}, 2)
	execute := make(chan struct{})
	results := make(chan error, 2)
	failure := errors.New("откат первого сценария")

	for _, input := range inputs {
		go func() {
			<-start
			err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				entered <- struct{}{}
				select {
				case <-execute:
				case <-ctx.Done():
					return ctx.Err()
				}

				found, err := tx.Buyers().Get(ctx, input.id)
				if err != nil || found.Phone != input.phone {
					return fmt.Errorf("изолированный поиск покупателя: %+v, %w", found, err)
				}
				challenge, err := tx.Challenges().FindByProviderRef(ctx, input.providerRef)
				if err != nil {
					return err
				}
				challenge.Observe(buyer.CallConfirmed, at(1))
				if err := tx.Challenges().Update(ctx, challenge); err != nil {
					return err
				}
				if fresh, err := tx.Inbox().MarkProcessed(ctx, input.providerRef, at(1)); err != nil || !fresh {
					return fmt.Errorf("входящее событие своего сценария: %v, %w", fresh, err)
				}
				if err := tx.Outbox().Enqueue(ctx, ports.NewTask{
					Kind:     "test",
					TenantID: "t1",
					DedupKey: input.providerRef,
					Payload:  []byte(`{}`),
					At:       at(1),
				}); err != nil {
					return err
				}
				if input.rollback {
					return failure
				}

				return nil
			})
			if input.rollback && errors.Is(err, failure) {
				err = nil
			} else if input.rollback && err == nil {
				err = errors.New("первый сценарий должен откатиться")
			}
			results <- err
		}()
	}
	close(start)

	for range 2 {
		select {
		case <-entered:
		case err := <-results:
			t.Fatal("обе холодные сессии должны открыться", err)
		case <-ctx.Done():
			t.Fatal("обе холодные сессии должны открыться", ctx.Err())
		}
	}
	close(execute)
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Error(err)
			}
		case <-ctx.Done():
			t.Fatal("изолированные сценарии должны завершиться", ctx.Err())
		}
	}

	if err := do(t, seed, func(ctx context.Context, tx ports.Tx) error {
		for _, input := range inputs {
			challenge, err := tx.Challenges().Get(ctx, input.id)
			if err != nil {
				return err
			}
			wantStatus, wantVersion := buyer.ChallengeConfirmed, int64(2)
			if input.rollback {
				wantStatus, wantVersion = buyer.ChallengePending, 1
			}
			if state := challenge.State(); state.Status != wantStatus || state.Version != wantVersion {
				return fmt.Errorf("commit и rollback не смешивают сессии: %+v", state)
			}
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var processed, outbox, xids int

	err := pool.QueryRow(ctx, selectGORMPilotScopeWrites, string(buyerB)).Scan(&processed, &outbox, &xids)
	if err != nil || processed != 1 || outbox != 1 || xids != 1 {
		t.Fatalf("GORM и raw фиксируются только в своей транзакции: %d/%d, xid=%d, %v", processed, outbox, xids, err)
	}
}

func TestGORMPilotPreservesAggregateTimestamps(t *testing.T) {
	for _, timestamp := range []time.Time{{}, at(0)} {
		t.Run(timestamp.Format(time.RFC3339), func(t *testing.T) {
			uow := postgres.NewUnitOfWork(testPool(t))
			challenge := buyer.NewChallenge(
				buyerB,
				"70000000001",
				buyer.CallInstruction{ProviderRef: "provider-1"},
				timestamp,
				5*time.Minute,
			)
			if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
				if err := tx.Buyers().Add(ctx, buyer.New(buyerA, "70000000001", timestamp)); err != nil {
					return err
				}

				return tx.Challenges().Add(ctx, challenge)
			}); err != nil {
				t.Fatal(err)
			}

			updated := timestamp
			if !timestamp.IsZero() {
				updated = at(1)
			}
			for _, update := range []bool{false, true} {
				if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
					found, err := tx.Buyers().Get(ctx, buyerA)
					if err != nil || !found.CreatedAt.Equal(timestamp) {
						return fmt.Errorf("время покупателя задаёт агрегат: %+v, %w", found, err)
					}
					loaded, err := tx.Challenges().Get(ctx, buyerB)
					if err != nil {
						return err
					}
					wantUpdated := timestamp
					if update {
						wantUpdated = updated
					}
					state := loaded.State()
					if !state.CreatedAt.Equal(timestamp) || !state.UpdatedAt.Equal(wantUpdated) {
						return fmt.Errorf("времена проверки задаёт агрегат: %+v", state)
					}
					if !update {
						loaded.Observe(buyer.CallPending, updated)

						return tx.Challenges().Update(ctx, loaded)
					}

					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestGORMPilotBuyerReadQueryCount(t *testing.T) {
	seedPool := testPool(t)
	seedBuyer(t, postgres.NewUnitOfWork(seedPool), buyerA, "70000000001")
	trace := &gormPilotQueryTrace{}
	config := seedPool.Config()
	config.ConnConfig.Tracer = trace
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	uow := postgres.NewUnitOfWork(pool)

	for range 2 {
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			found, err := tx.Buyers().Get(ctx, buyerA)
			if err != nil || found.ID != buyerA {
				return fmt.Errorf("покупатель по идентификатору: %+v, %w", found, err)
			}

			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	trace.mu.Lock()
	defer trace.mu.Unlock()

	if operations := strings.Join(trace.operations, ","); operations != "begin,select,commit,begin,select,commit" {
		t.Fatalf("холодный и прогретый Get выполняют по одному SELECT без служебных запросов ORM: %s", operations)
	}
}

func TestGORMPilotChallengeZeroAndNullUpdates(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	challenge := gormPilotChallenge()
	challenge.Observe(buyer.CallPending, at(1))

	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Challenges().Add(ctx, challenge)
	}); err != nil {
		t.Fatal(err)
	}

	// Нулевые поля снимка должны попадать в UPDATE: Updates(struct) их пропускает.
	state := challenge.State()
	state.Checks = 0
	state.LastCheckedAt = time.Time{}
	restored := buyer.RestoreChallenge(state)
	if !restored.Expire(at(10)) {
		t.Fatal("проверка истекла")
	}

	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Challenges().Update(ctx, restored)
	}); err != nil {
		t.Fatal(err)
	}

	var (
		checks    int
		lastIsNil bool
	)
	err := pool.QueryRow(context.Background(), selectGORMPilotChallengeCounters, string(buyerB)).Scan(
		&checks,
		&lastIsNil,
	)
	if err != nil || checks != 0 || !lastIsNil {
		t.Fatalf("явно сохраняются 0 и SQL NULL: checks=%d, null=%v, %v", checks, lastIsNil, err)
	}

	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		loaded, err := tx.Challenges().Get(ctx, buyerB)
		if err != nil {
			return err
		}
		state := loaded.State()
		if state.Checks != 0 || !state.LastCheckedAt.IsZero() || state.Status != buyer.ChallengeExpired {
			return fmt.Errorf("нулевые значения после чтения: %+v", state)
		}
		status, version := loaded.Origin()
		if status != state.Status || version != state.Version || state.Version != 2 {
			return fmt.Errorf("исходная версия после чтения: %s/%d, %+v", status, version, state)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGORMPilotMissingLookupContracts(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))

	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		if _, err := tx.Buyers().FindByPhone(ctx, "70000000001"); !errors.Is(err, buyer.ErrNotFound) {
			return fmt.Errorf("отсутствующий телефон: %w", err)
		}
		if _, err := tx.Challenges().FindByProviderRef(ctx, "missing"); !errors.Is(err, buyer.ErrChallengeNotFound) {
			return fmt.Errorf("отсутствующая проверка шлюза: %w", err)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func gormPilotChallenge() *buyer.Challenge {
	return buyer.NewChallenge(
		buyerB,
		"70000000001",
		buyer.CallInstruction{
			ProviderRef:     "provider-1",
			CallPhone:       "78000000001",
			CallPhonePretty: "+7 (800) 000-00-01",
		},
		at(0),
		5*time.Minute,
	)
}

func gormPilotWrite(ctx context.Context, tx ports.Tx) error {
	if err := tx.Buyers().Add(ctx, buyer.New(buyerA, "70000000001", at(0))); err != nil {
		return err
	}
	if err := tx.Challenges().Add(ctx, gormPilotChallenge()); err != nil {
		return err
	}
	if fresh, err := tx.Inbox().MarkProcessed(ctx, "topic:0:1", at(0)); err != nil || !fresh {
		return fmt.Errorf("отметка входящего события: %v, %w", fresh, err)
	}

	return tx.Outbox().Enqueue(ctx, ports.NewTask{
		Kind:     "test",
		TenantID: "t1",
		DedupKey: "task-1",
		Payload:  []byte(`{"buyerId":"` + string(buyerA) + `"}`),
		At:       at(0),
	})
}

func gormPilotExpectWrites(ctx context.Context, pool *pgxpool.Pool, want int) error {
	var actual gormPilotWrites

	err := pool.QueryRow(ctx, selectGORMPilotWrites).Scan(
		&actual.buyers,
		&actual.challenges,
		&actual.processed,
		&actual.outbox,
		&actual.xids,
	)
	if err != nil {
		return err
	}
	expected := gormPilotWrites{
		buyers:     want,
		challenges: want,
		processed:  want,
		outbox:     want,
		xids:       want,
	}
	if actual != expected {
		return fmt.Errorf("ожидалось по %d записи с общим xmin: %+v", want, actual)
	}

	return nil
}

func (trace *gormPilotQueryTrace) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	trace.mu.Lock()
	defer trace.mu.Unlock()

	if fields := strings.Fields(data.SQL); len(fields) != 0 {
		trace.operations = append(trace.operations, strings.ToLower(fields[0]))
	}

	return ctx
}

func (*gormPilotQueryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
