package moderation_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestStatusQueryMatchesEvents(t *testing.T) {
	for _, mode := range []string{"pending", "allow", "reject", "unpublished", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			e := setup()
			ctx := context.Background()
			publisher := &fake.Publisher{}
			service := moderationstatus.NewService(e.store, e.clock, publisher, "offer", "participant")
			e.project(t, "p1", 1)
			if mode == "allow" || mode == "reject" {
				c := e.view(t, "p1").Case
				decide := e.service.Allow
				if mode == "reject" {
					decide = e.service.Reject
				}
				_, err := decide(ctx, appmoderation.DecisionCommand{
					CaseID:          c.ID,
					ExpectedVersion: c.Version,
					Note:            "Решение оператора",
					Operator:        moderation.Actor{ID: "op1", Name: "Оператор"},
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unpublished" || mode == "deleted" {
				state, _ := e.store.Product("t1", "p1")
				state.Card.Publication.Published = false
				state.Deleted = mode == "deleted"
				e.store.SeedProduct(state)
			}
			task := moderationstatus.OfferTask("t1", "p1", e.clock.Now())
			if err := service.HandleTask(ctx, ports.Task{
				Kind:     task.Kind,
				TenantID: task.TenantID,
				Payload:  task.Payload,
			}); err != nil {
				t.Fatal(err)
			}
			before := e.store.OutboxRows()
			messages := publisher.Messages()
			snapshot, err := service.Get(ctx, "t1", []ids.ProductID{"p1", "missing"})
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Items) != len(messages) {
				t.Fatal(snapshot, messages)
			}
			if len(messages) == 1 {
				var event moderationstatus.OfferEvent
				if err := json.Unmarshal(messages[0].Value, &event); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(&event, snapshot.Items[0]) {
					t.Fatalf("событие: %+v; запрос: %+v", event, snapshot.Items[0])
				}
			}
			if !reflect.DeepEqual(before, e.store.OutboxRows()) || len(publisher.Messages()) != len(messages) {
				t.Fatal("чтение изменило outbox или опубликовало событие")
			}
		})
	}
}

func TestParticipantQueryMatchesLatestSystemDecision(t *testing.T) {
	e := setup()
	ctx := context.Background()
	publisher := &fake.Publisher{}
	service := moderationstatus.NewService(e.store, e.clock, publisher, "offer", "participant")
	if err := e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.ParticipantDecisions().Add(ctx, participant.DecisionRecord{
			ID:       "00000000-0000-4000-8000-000000000001",
			TenantID: "t1",
			Actor:    "system",
			Action:   "trust",
			Value:    participant.Trusted,
			At:       e.clock.Now(),
			Decision: participant.Decision{
				Reason:        "checked",
				ReasonMessage: "Проверено",
				Note:          "Одобрены предложения",
			},
		})
	}); err != nil {
		t.Fatal(err)
	}
	task := moderationstatus.ParticipantTask("t1", e.clock.Now())
	if err := service.HandleTask(ctx, ports.Task{
		Kind:     task.Kind,
		TenantID: task.TenantID,
		Payload:  task.Payload,
	}); err != nil {
		t.Fatal(err)
	}
	var event moderationstatus.ParticipantEvent
	if err := json.Unmarshal(publisher.Messages()[0].Value, &event); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Get(ctx, "t1", nil)
	if err != nil || !reflect.DeepEqual(snapshot.Participant, &event) {
		t.Fatal(snapshot, event, err)
	}
	if snapshot.Participant.By != "system" || snapshot.Participant.Reason.Code != "checked" {
		t.Fatal(snapshot.Participant)
	}
	absent, err := service.Get(ctx, "another-tenant", []ids.ProductID{"p1"})
	if err != nil || absent.Participant != nil || len(absent.Items) != 0 {
		t.Fatal(absent, err)
	}
}
