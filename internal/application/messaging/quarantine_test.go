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

func TestDispatcherStopsPoisonTaskAndReleasesTenant(t *testing.T) {
	for _, batch := range []bool{false, true} {
		store, clock := fake.NewStore(), fake.NewClock(t0)
		enqueue(t, store, "poison", "t1", "bad", t0)
		enqueue(t, store, "unknown", "t2", "unknown", t0)
		d := messaging.NewDispatcher(store, clock, config(), map[string]messaging.Handler{
			"poison": func(context.Context, ports.Task) error {
				return errs.New(errs.Invalid, "test.invalid", "некорректная задача")
			},
			"ok": func(context.Context, ports.Task) error { return nil },
		})
		if batch {
			d.RegisterBatch("poison", func(context.Context, []ports.Task) []error {
				return []error{errs.New(errs.Invalid, "test.invalid", "некорректная задача")}
			})
		}
		if _, err := d.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		clock.Advance(24 * time.Hour)
		enqueue(t, store, "ok", "t1", "good", clock.Now())
		outcomes, err := d.RunOnce(context.Background())
		if err != nil || len(outcomes) != 1 || outcomes[0].Task.Kind != "ok" || outcomes[0].Err != nil {
			t.Fatalf("ядовитые задачи больше не занимают квоту: %+v, %v", outcomes, err)
		}
		if len(store.OutboxRows()) != 3 {
			t.Fatal("карантин не теряет исходные задачи")
		}
	}
}

func TestDispatcherExhaustsUnclassifiedFailures(t *testing.T) {
	store, clock := fake.NewStore(), fake.NewClock(t0)
	enqueue(t, store, "fail", "t1", "bad", t0)
	d := messaging.NewDispatcher(store, clock, config(), map[string]messaging.Handler{
		"fail": func(context.Context, ports.Task) error { return errors.New("сеть недоступна") },
	})
	for range 10 {
		outcomes, err := d.RunOnce(context.Background())
		if err != nil || len(outcomes) != 1 {
			t.Fatal(outcomes, err)
		}
		clock.Advance(24 * time.Hour)
	}
	outcomes, err := d.RunOnce(context.Background())
	if err != nil || len(outcomes) != 0 {
		t.Fatalf("исчерпавшая попытки задача ждёт оператора: %+v, %v", outcomes, err)
	}
}

func TestDependencyOutageRecoversAllTasksAfterThirtyMinutes(t *testing.T) {
	for _, batch := range []bool{false, true} {
		store, clock := fake.NewStore(), fake.NewClock(t0)
		outage := true
		handlers := map[string]messaging.Handler{}
		for _, kind := range []string{"index", "status", "reconcile"} {
			for _, tenant := range []string{"t1", "t2"} {
				enqueue(t, store, kind, ids.TenantID(tenant), kind+tenant, t0)
			}
			handlers[kind] = func(context.Context, ports.Task) error {
				if outage {
					return errs.New(errs.Unavailable, "test.dependency", "зависимость недоступна")
				}
				return nil
			}
		}
		cfg := config()
		d := messaging.NewDispatcher(store, clock, cfg, handlers)
		if batch {
			d.RegisterBatch("index", func(ctx context.Context, tasks []ports.Task) []error {
				result := make([]error, len(tasks))
				for i, task := range tasks {
					result[i] = handlers["index"](ctx, task)
				}
				return result
			})
		}
		handlers["independent"] = func(context.Context, ports.Task) error { return nil }
		for second := 0; second < 1800; second++ {
			if second == 100 {
				enqueue(t, store, "independent", "t1", "same-store", clock.Now())
				enqueue(t, store, "independent", "t3", "other-store", clock.Now())
			}
			if _, err := d.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			clock.Advance(time.Second)
		}
		rows := store.OutboxRows()
		if rows[6].DoneAt == nil || rows[7].DoneAt == nil {
			t.Fatal("повтор удерживает соседей своего или другого магазина", rows)
		}
		outage = false
		for range 601 {
			if _, err := d.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			clock.Advance(time.Second)
		}
		for _, row := range store.OutboxRows() {
			if row.DoneAt == nil || row.FailedAt != nil {
				t.Fatalf("корректная задача не восстановилась: %+v", row)
			}
		}
	}
}

func TestReplayRejectsMalformedIntentBeforeReadingEvent(t *testing.T) {
	service := messaging.NewEvents(fake.NewStore(), fake.NewClock(t0), &fake.Replay{})
	for _, payload := range []string{"{", "{}", `{"eventId":1,"decisionVersion":0}`} {
		err := service.HandleTask(context.Background(), ports.Task{Payload: []byte(payload)})
		if !errors.Is(err, messaging.ErrInvalidTask) {
			t.Fatal("повреждённое намерение должно иметь диагностику задачи", payload, err)
		}
	}
}

func TestDispatcherQuarantinesPanickingTaskAndKeepsNeighbours(t *testing.T) {
	for _, batch := range []bool{false, true} {
		store, clock := fake.NewStore(), fake.NewClock(t0)
		enqueue(t, store, "panic", "t1", "bad", t0)
		enqueue(t, store, "ok", "t2", "good", t0)
		d := messaging.NewDispatcher(
			store,
			clock,
			messaging.DispatcherConfig{Batch: 10, Lease: time.Minute, MaxAttempts: 1},
			map[string]messaging.Handler{
				"panic": func(context.Context, ports.Task) error { panic("сбой обработчика") },
				"ok":    func(context.Context, ports.Task) error { return nil },
			},
		)
		if batch {
			d.RegisterBatch("panic", func(context.Context, []ports.Task) []error { panic("сбой пачки") })
		}
		outcomes, err := d.RunOnce(context.Background())
		if err != nil || len(outcomes) != 2 {
			t.Fatalf("паника не прерывает проход: %+v, %v", outcomes, err)
		}
		for _, outcome := range outcomes {
			switch outcome.Task.Kind {
			case "ok":
				if outcome.Err != nil {
					t.Fatalf("соседняя задача выполнена: %v", outcome.Err)
				}
			case "panic":
				if !errors.Is(outcome.Err, errs.ErrPanic) || outcome.FailureReason != "attempts_exhausted" {
					t.Fatalf("паника — неизвестный сбой, исчерпавший попытки: %+v", outcome)
				}
				if _, ok := errs.PanicStack(outcome.Err); !ok {
					t.Fatal("итог сохраняет стек для журнала")
				}
			}
		}
		for _, row := range store.OutboxRows() {
			if row.Kind == "panic" && row.FailedAt == nil {
				t.Fatalf("задача с паникой в карантине: %+v", row)
			}
		}
	}
}
