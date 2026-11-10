package moderation_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func statusTask(t *testing.T, e environment, kind string) ports.Task {
	t.Helper()
	var found []ports.Task
	for _, row := range e.store.OutboxRows() {
		if row.Kind == kind && row.DoneAt == nil {
			found = append(found, ports.Task{ID: row.ID, Kind: row.Kind, TenantID: row.TenantID, Payload: row.Payload})
		}
	}
	if len(found) != 1 {
		t.Fatalf("ожидалась одна склеенная задача %s: %d", kind, len(found))
	}
	return found[0]
}

func completeStatusTask(t *testing.T, e environment, task ports.Task) {
	t.Helper()

	ctx := context.Background()
	tasks, err := e.store.Claim(ctx, ports.ClaimRequest{
		Now:        e.clock.Now(),
		Limit:      100,
		PerTenant:  100,
		LeaseUntil: e.clock.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, claimed := range tasks {
		if claimed.ID == task.ID {
			if err := e.store.Complete(ctx, claimed.ID, claimed.Attempts, e.clock.Now()); err != nil {
				t.Fatal(err)
			}

			return
		}
	}

	t.Fatalf("задача %d не взята для завершения", task.ID)
}

func TestStatusEvents(t *testing.T) {
	e := setup()
	ctx := context.Background()
	publisher := &fake.Publisher{}
	service := moderationstatus.NewService(e.store, e.clock, publisher, "p1.offer", "p1.participant")
	e.project(t, "p1", 1)
	task := statusTask(t, e, moderationstatus.TaskOffer)
	if err := service.HandleTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	first := publisher.Messages()[0]
	var pending moderationstatus.OfferEvent
	if err := json.Unmarshal(first.Value, &pending); err != nil {
		t.Fatal(err)
	}
	if first.Topic != "p1.offer" || string(first.Key) != `["t1","p1"]` || first.Headers["x-tenant"] != "t1" {
		t.Fatal(first)
	}
	if pending.SchemaVersion != 1 || pending.Revision != 1 || pending.Status != "pending" || pending.Visible {
		t.Fatal(pending)
	}
	if pending.At != e.clock.Now().UTC().Format(time.RFC3339) {
		t.Fatal(pending.At)
	}
	completeStatusTask(t, e, task)
	c := e.view(t, "p1").Case
	cmd := appmoderation.DecisionCommand{
		CaseID:          c.ID,
		ExpectedVersion: c.Version,
		Note:            "Проверено",
		IdempotencyKey:  "k1",
	}
	if _, err := e.service.Allow(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	task = statusTask(t, e, moderationstatus.TaskOffer)
	if err := service.HandleTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	var allowed moderationstatus.OfferEvent
	if err := json.Unmarshal(publisher.Messages()[1].Value, &allowed); err != nil {
		t.Fatal(err)
	}
	if allowed.Decision == nil || allowed.Decision.Action != moderation.Allow ||
		allowed.Status != "published" || !allowed.Visible || allowed.Reasons == nil ||
		len(allowed.Reasons) != 0 {
		t.Fatal(allowed)
	}
	before := e.store.OutboxRows()
	e.project(t, "p1", 1)
	if _, err := e.service.Allow(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if err := e.service.Reevaluate(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, e.store.OutboxRows()) {
		t.Fatal("повтор изменил очередь")
	}
	publisher.Err = errors.New("брокер недоступен")
	if err := service.HandleTask(ctx, task); !errors.Is(err, publisher.Err) {
		t.Fatal(err)
	}
	publisher.Err = nil
	if err := service.HandleTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(publisher.Messages()[1], publisher.Messages()[2]) {
		t.Fatal("повтор публикации изменил состояние")
	}
	members := appparticipant.NewService(e.store, e.clock, &fake.IDs{})
	if err := e.service.AddReason(ctx, moderation.ReasonCode{Code: "r1", Text: "Текст"}); err != nil {
		t.Fatal(err)
	}
	decision := participant.Decision{Reason: "r1", Note: "Проверено"}
	if _, err := members.SetTrust(ctx, "t1", participant.Trusted, decision); err != nil {
		t.Fatal(err)
	}
	if err := e.service.PutReason(ctx, moderation.ReasonCode{Code: "r1", Text: "Изменено"}); err != nil {
		t.Fatal(err)
	}
	task = statusTask(t, e, moderationstatus.TaskParticipant)
	if err := service.HandleTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	message := publisher.Messages()[3]
	var member moderationstatus.ParticipantEvent
	if err := json.Unmarshal(message.Value, &member); err != nil {
		t.Fatal(err)
	}
	if message.Topic != "p1.participant" || string(message.Key) != "t1" || message.Headers["x-tenant"] != "t1" {
		t.Fatal(message)
	}
	if member.SchemaVersion != 1 || !member.Enabled || member.Trust != participant.Trusted ||
		member.Reason == nil || member.Reason.Code != decision.Reason || member.By != "operator" ||
		member.Note != decision.Note || member.At != pending.At || member.Reason.Text != "Текст" {
		t.Fatal(member)
	}
	before = e.store.OutboxRows()
	if _, err := members.SetTrust(ctx, "t1", participant.Trusted, decision); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, e.store.OutboxRows()) {
		t.Fatal("повтор доверия изменил очередь")
	}
}

func TestStatusTasksCoalesceAndSkipDeleted(t *testing.T) {
	e := setup()
	ctx := context.Background()
	publisher := &fake.Publisher{}
	service := moderationstatus.NewService(e.store, e.clock, publisher, "offer", "participant")
	e.project(t, "p1", 1)
	e.project(t, "p1", 2)
	task := statusTask(t, e, moderationstatus.TaskOffer)
	if err := service.HandleTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	var event moderationstatus.OfferEvent
	if err := json.Unmarshal(publisher.Messages()[0].Value, &event); err != nil || event.Revision != 2 {
		t.Fatal(event, err)
	}
	state, _ := e.store.Product("t1", "p1")
	state.Deleted = true
	e.store.SeedProduct(state)
	if err := service.HandleTask(ctx, task); err != nil || len(publisher.Messages()) != 1 {
		t.Fatal("удалённый товар породил событие", err)
	}
}

func TestParticipantStatusLatestDecisionAndRollback(t *testing.T) {
	e := setup()
	ctx := context.Background()
	publisher := &fake.Publisher{}
	service := moderationstatus.NewService(e.store, e.clock, publisher, "offer", "participant")
	members := appparticipant.NewService(e.store, e.clock, &fake.IDs{})
	if err := e.service.AddReason(ctx, moderation.ReasonCode{Code: "r1", Text: "Текст"}); err != nil {
		t.Fatal(err)
	}
	decision := participant.Decision{Reason: "r1", Note: "Проверка"}
	e.store.FailCommit = errors.New("сбой фиксации")
	if _, err := members.Disable(ctx, "t1", decision); err == nil {
		t.Fatal("ожидался откат")
	}
	e.store.FailCommit = nil
	if len(e.store.OutboxRows()) != 0 {
		t.Fatal("откат оставил задачу")
	}
	if _, err := members.Disable(ctx, "t1", decision); err != nil {
		t.Fatal(err)
	}
	task := statusTask(t, e, moderationstatus.TaskParticipant)
	if err := service.HandleTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	var disabled moderationstatus.ParticipantEvent
	if err := json.Unmarshal(publisher.Messages()[0].Value, &disabled); err != nil || disabled.Enabled {
		t.Fatal(disabled, err)
	}
	e.clock.Advance(time.Second)
	state, ok := e.store.Participant("t1")
	if !ok {
		t.Fatal("участник t1 не найден")
	}
	state.StoreCard.Readiness.Ready = true
	e.store.SeedParticipant(state)
	decision = participant.Decision{Reason: "r1", Note: "Проверено"}
	if _, err := members.Enable(ctx, "t1", decision); err != nil {
		t.Fatal(err)
	}
	task = statusTask(t, e, moderationstatus.TaskParticipant)
	if err := service.HandleTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	var enabled moderationstatus.ParticipantEvent
	if err := json.Unmarshal(publisher.Messages()[1].Value, &enabled); err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || enabled.Reason == nil || enabled.Reason.Code != decision.Reason ||
		enabled.By != "operator" || enabled.Note != decision.Note {
		t.Fatal("не получено последнее решение", enabled)
	}
}
