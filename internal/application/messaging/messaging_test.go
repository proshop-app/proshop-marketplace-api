package messaging_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

var t0 = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

func enqueue(t *testing.T, store *fake.Store, kind string, tenant ids.TenantID, key string, at time.Time) {
	t.Helper()

	err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().
			Enqueue(ctx, ports.NewTask{Kind: kind, TenantID: tenant, DedupKey: key, Payload: []byte(`{}`), At: at})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func config() messaging.DispatcherConfig {
	return messaging.DispatcherConfig{Batch: 10, PerTenant: 2, Lease: time.Minute}
}

func TestDispatcherCompletesAndRetries(t *testing.T) {
	store, clock := fake.NewStore(), fake.NewClock(t0)
	enqueue(t, store, "ok", "t1", "a", t0)
	enqueue(t, store, "fail", "t1", "b", t0)
	enqueue(t, store, "unknown", "t2", "c", t0)

	calls := 0
	dispatcher := messaging.NewDispatcher(store, clock, config(), map[string]messaging.Handler{
		"ok":   func(context.Context, ports.Task) error { calls++; return nil },
		"fail": func(context.Context, ports.Task) error { return errors.New("сбой") },
	})

	clock.Advance(3 * time.Second)

	outcomes, err := dispatcher.RunOnce(context.Background())
	if err != nil || len(outcomes) != 3 || calls != 1 {
		t.Fatalf("итоги: %+v, %v, вызовов %d", outcomes, err, calls)
	}
	if outcomes[0].Lag != 3*time.Second {
		t.Fatalf("задержка задачи: %s", outcomes[0].Lag)
	}
	if !errors.Is(outcomes[2].Err, messaging.ErrUnknownTask) {
		t.Fatalf("неизвестная задача: %v", outcomes[2].Err)
	}

	rows := store.OutboxRows()
	if rows[0].DoneAt == nil || rows[1].DoneAt != nil || rows[1].LastError != "сбой" {
		t.Fatalf("строки: %+v", rows)
	}
	if want := t0.Add(3*time.Second + time.Second); !rows[1].NextAt.Equal(want) {
		t.Fatalf("первый повтор через секунду: %s, ожидалось %s", rows[1].NextAt, want)
	}

	outcomes, err = dispatcher.RunOnce(context.Background())
	if err != nil || len(outcomes) != 0 {
		t.Fatalf("отложенные задачи ещё не созрели: %+v, %v", outcomes, err)
	}
}

func TestDispatcherLimitsPerTenant(t *testing.T) {
	store, clock := fake.NewStore(), fake.NewClock(t0)
	for _, key := range []string{"1", "2", "3", "4"} {
		enqueue(t, store, "ok", "noisy", key, t0)
	}
	enqueue(t, store, "ok", "quiet", "q", t0.Add(time.Second))
	clock.Advance(time.Minute)

	seen := map[ids.TenantID]int{}
	dispatcher := messaging.NewDispatcher(store, clock, config(), map[string]messaging.Handler{
		"ok": func(_ context.Context, task ports.Task) error { seen[task.TenantID]++; return nil },
	})

	if _, err := dispatcher.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen["noisy"] != 2 || seen["quiet"] != 1 {
		t.Fatalf("шумный магазин не занимает всю пачку: %+v", seen)
	}
}

func TestDispatcherRejectsStaleClaim(t *testing.T) {
	for _, failure := range []error{nil, errors.New("сбой устаревшего обработчика")} {
		name := "complete"
		if failure != nil {
			name = "retry"
		}
		t.Run(name, func(t *testing.T) {
			store, clock := fake.NewStore(), fake.NewClock(t0)
			ctx := context.Background()
			enqueue(t, store, "test", "t1", "k1", t0)
			var current ports.Task

			dispatcher := messaging.NewDispatcher(store, clock, config(), map[string]messaging.Handler{
				"test": func(ctx context.Context, stale ports.Task) error {
					clock.Advance(2 * time.Minute)
					tasks, err := store.Claim(ctx, ports.ClaimRequest{
						Now:        clock.Now(),
						Limit:      1,
						PerTenant:  1,
						LeaseUntil: clock.Now().Add(time.Minute),
					})
					if err != nil || len(tasks) != 1 || tasks[0].ID != stale.ID ||
						tasks[0].Attempts != stale.Attempts+1 {
						t.Fatalf("задачу должен взять другой обработчик: %+v, %v", tasks, err)
					}
					current = tasks[0]

					return failure
				},
			})

			outcomes, err := dispatcher.RunOnce(ctx)
			if !errors.Is(err, ports.ErrTaskClaimConflict) || errs.CodeOf(err) != errs.Conflict || len(outcomes) != 0 {
				t.Fatalf("устаревший результат должен вернуть конфликт: %+v, %v", outcomes, err)
			}
			row := store.OutboxRows()[0]
			if row.DoneAt != nil || row.LastError != "" || row.Attempts != current.Attempts ||
				!row.NextAt.Equal(clock.Now().Add(time.Minute)) {
				t.Fatalf("устаревший результат изменил задачу текущего владельца: %+v", row)
			}
			if err := store.Complete(ctx, current.ID, current.Attempts, clock.Now()); err != nil {
				t.Fatalf("текущий владелец должен завершить задачу: %v", err)
			}
		})
	}
}

func TestBackoff(t *testing.T) {
	cases := map[int]time.Duration{
		0:   time.Second,
		1:   time.Second,
		2:   2 * time.Second,
		5:   16 * time.Second,
		10:  512 * time.Second,
		11:  messaging.MaxBackoff,
		100: messaging.MaxBackoff,
	}
	for attempts, want := range cases {
		if got := messaging.Backoff(attempts); got != want {
			t.Fatalf("попытка %d: %s, ожидалось %s", attempts, got, want)
		}
	}
}

func TestParkerIsIdempotent(t *testing.T) {
	store := fake.NewStore()
	parker := messaging.NewParker(store)
	event := ports.ParkedEvent{Topic: "x", Partition: 1, Offset: 7, Reason: "tenant_missing", ParkedAt: t0}

	for range 2 {
		if err := parker.Park(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}

	if parked := store.ParkedEvents(); len(parked) != 1 || parked[0].Reason != "tenant_missing" {
		t.Fatalf("отравленные: %+v", parked)
	}
}

func TestJanitorPurgesByPolicy(t *testing.T) {
	store, clock := fake.NewStore(), fake.NewClock(t0)
	ctx := context.Background()

	_ = store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		_, _ = tx.Inbox().MarkProcessed(ctx, "old", t0.Add(-15*24*time.Hour))
		_, _ = tx.Inbox().MarkProcessed(ctx, "new", t0)

		return tx.Parked().Park(ctx, ports.ParkedEvent{Topic: "x", Offset: 1, ParkedAt: t0.Add(-31 * 24 * time.Hour)})
	})

	enqueue(t, store, "ok", "t1", "a", t0.Add(-4*24*time.Hour))
	dispatcher := messaging.NewDispatcher(
		store,
		fake.NewClock(t0.Add(-4*24*time.Hour)),
		config(),
		map[string]messaging.Handler{
			"ok": func(context.Context, ports.Task) error { return nil },
		},
	)
	if _, err := dispatcher.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	janitor := messaging.NewJanitor(store, clock, messaging.RetentionPolicy{
		ProcessedEvents: 14 * 24 * time.Hour,
		OutboxDone:      3 * 24 * time.Hour,
		ParkedEvents:    30 * 24 * time.Hour,
		Batch:           100,
	})

	result, err := janitor.Purge(ctx)
	if err != nil || result.Processed != 1 || result.OutboxDone != 1 || result.Parked != 1 {
		t.Fatalf("чистка: %+v, %v", result, err)
	}
	if !store.Processed("new") {
		t.Fatal("свежие отметки остаются")
	}
}
