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

func TestDispatcherExhaustsTransientFailures(t *testing.T) {
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

func TestReplayRejectsMalformedIntentBeforeReadingEvent(t *testing.T) {
	service := messaging.NewEvents(fake.NewStore(), fake.NewClock(t0), &fake.Replay{})
	for _, payload := range []string{"{", "{}", `{"eventId":1,"decisionVersion":0}`} {
		err := service.HandleTask(context.Background(), ports.Task{Payload: []byte(payload)})
		if !errors.Is(err, messaging.ErrInvalidTask) {
			t.Fatal("повреждённое намерение должно иметь диагностику задачи", payload, err)
		}
	}
}
