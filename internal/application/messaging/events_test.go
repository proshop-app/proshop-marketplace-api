package messaging_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/event"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
)

func TestEventDecisionsAreAtomicAuthorizedAndReplayable(t *testing.T) {
	ctx := context.Background()
	clock := fake.NewClock(time.Now())
	store := fake.NewStore()
	replay := &fake.Replay{}
	service := messaging.NewEvents(store, clock, replay)
	actor := operator.Principal{ID: "op1", Role: operator.Role}
	for i := int64(0); i < 3; i++ {
		if err := messaging.NewParker(store).Park(ctx, ports.ParkedEvent{
			Topic:       "catalog.product.projected",
			Offset:      i,
			TenantID:    "t1",
			ParkedAt:    clock.Now(),
			PublishedAt: clock.Now().Add(-time.Minute),
			Value:       []byte(`{"version":1}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	viewer := operator.Principal{ID: "viewer", Role: "viewer"}
	_, err := service.List(ctx, viewer, ports.EventFilter{})
	if !errors.Is(err, operator.ErrForbidden) {
		t.Fatal(err)
	}
	_, err = service.Decide(ctx, operator.Principal{}, 1, true, "повтор")
	if !errors.Is(err, operator.ErrUnauthorized) {
		t.Fatal(err)
	}
	if _, err := service.Decide(ctx, actor, 1, false, "  "); !errors.Is(err, event.ErrNote) {
		t.Fatal(err)
	}
	store.FailCommit = errors.New("commit")
	if _, err := service.Decide(ctx, actor, 1, true, "повтор"); err == nil {
		t.Fatal("нужен откат")
	}
	store.FailCommit = nil
	if len(store.OutboxRows()) != 0 {
		t.Fatal("задача откатилась вместе с решением")
	}
	queued, err := service.Decide(ctx, actor, 1, true, "повтор после исправления")
	if err != nil || queued.Control.Status != event.Queued || queued.Control.OperatorID != actor.ID {
		t.Fatal(queued, err)
	}
	if _, err := service.Decide(ctx, actor, 1, false, "закрыть"); !errors.Is(err, event.ErrConflict) {
		t.Fatal(err)
	}
	rows := store.OutboxRows()
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	task := ports.Task{Kind: rows[0].Kind, Payload: rows[0].Payload, Attempts: 1}
	if err := service.HandleTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if len(replay.Events) != 1 || replay.Events[0].Offset != 0 || replay.Events[0].PublishedAt.IsZero() {
		t.Fatal("исходная идентичность", replay.Events)
	}
	if err := service.HandleTask(ctx, task); err != nil || len(replay.Events) != 1 {
		t.Fatal("повтор задачи", err)
	}
	if _, err := service.Decide(ctx, actor, 2, false, "разобрано вручную"); err != nil {
		t.Fatal(err)
	}
	page, err := service.List(ctx, actor, ports.EventFilter{Status: event.Resolved, TenantID: "t1", Limit: 1})
	if err != nil || page.Total != 2 || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	page, err = service.List(ctx, actor, ports.EventFilter{Status: event.Resolved, Limit: 1, Offset: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Control.ID != 1 {
		t.Fatal(page, err)
	}
	if _, err := service.Decide(ctx, actor, 3, true, "проверить"); err != nil {
		t.Fatal(err)
	}
	replay.Err = event.ErrFilter
	row := store.OutboxRows()[1]
	if err := service.HandleTask(ctx, ports.Task{Payload: row.Payload, Attempts: 1}); err != nil {
		t.Fatal(err)
	}
	page, err = service.List(ctx, actor, ports.EventFilter{Status: event.Parked})
	if err != nil || page.Total != 1 {
		t.Fatal("невалидное событие снова припарковано", page, err)
	}
}
