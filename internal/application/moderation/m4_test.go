package moderation_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestReasonDictionaryAndDecisionSnapshot(t *testing.T) {
	e := setup()
	ctx := context.Background()
	reason := moderation.ReasonCode{Code: "r1", Text: "Текст"}
	if err := e.service.PutReason(ctx, reason); !errors.Is(err, moderation.ErrNotFound) {
		t.Fatal(err)
	}
	if err := e.service.AddReason(ctx, reason); err != nil {
		t.Fatal(err)
	}
	if err := e.service.AddReason(ctx, reason); !errors.Is(err, moderation.ErrReasonExists) {
		t.Fatal(err)
	}
	e.project(t, "p1", 1)
	c := e.view(t, "p1").Case
	cmd := appmoderation.DecisionCommand{
		CaseID:          c.ID,
		ExpectedVersion: c.Version,
		Reason:          "r2",
		Note:            "Заметка",
	}
	if _, err := e.service.Allow(ctx, cmd); !errors.Is(err, moderation.ErrReasonUnknown) {
		t.Fatal(err)
	}
	if _, err := e.service.Reject(ctx, cmd); !errors.Is(err, moderation.ErrReasonUnknown) {
		t.Fatal(err)
	}
	cmd.Reason = "r1"
	if _, err := e.service.Reject(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	reason.Text = "Изменено"
	if err := e.service.PutReason(ctx, reason); err != nil {
		t.Fatal(err)
	}
	view := e.view(t, "p1")
	if len(view.History) != 1 || view.History[0].ReasonMessage != "Текст" {
		t.Fatal(view)
	}
	publisher := &fake.Publisher{}
	service := moderationstatus.NewService(e.store, e.clock, publisher, "offer", "participant")
	if err := service.HandleTask(ctx, statusTask(t, e, moderationstatus.TaskOffer)); err != nil {
		t.Fatal(err)
	}
	var event moderationstatus.OfferEvent
	if err := json.Unmarshal(publisher.Messages()[0].Value, &event); err != nil {
		t.Fatal(err)
	}
	if event.Decision == nil || event.Decision.Reason == nil || event.Decision.Reason.Text != "Текст" ||
		len(event.Reasons) != 1 || event.Reasons[0].Key != "moderation.probation" {
		t.Fatal(event)
	}
	e.project(t, "p1", 2)
	if err := service.HandleTask(ctx, statusTask(t, e, moderationstatus.TaskOffer)); err != nil {
		t.Fatal(err)
	}
	event = moderationstatus.OfferEvent{}
	if err := json.Unmarshal(publisher.Messages()[1].Value, &event); err != nil {
		t.Fatal(err)
	}
	if event.Decision != nil || event.Status != "pending" {
		t.Fatal(event)
	}
}

func TestClosedCaseCardAndLegacyFallback(t *testing.T) {
	e := setup()
	ctx := context.Background()
	e.project(t, "p1", 1)
	c := e.view(t, "p1").Case
	cmd := appmoderation.DecisionCommand{CaseID: c.ID, ExpectedVersion: c.Version, Note: "Заметка"}
	if _, err := e.service.Allow(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	product, _ := e.store.Product("t1", "p1")
	product.Card.Name = "Изменено"
	e.store.SeedProduct(product)
	details, err := e.service.CaseDetails(ctx, c.ID)
	if err != nil || details.Offer.Name != "Товар" || details.Offer.BasePrice != 100 {
		t.Fatal(details, err)
	}
	legacyID := ids.UUID("00000000-0000-4000-8000-999999999999")
	err = e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		v := moderation.VerdictState{TenantID: "t1", ProductID: "p1", Revision: 1, Status: moderation.Pending}
		legacy := moderation.NewCase(legacyID, v, moderation.Rule, "", e.clock.Now(), nil)
		return tx.ModerationCases().Save(ctx, legacy)
	})
	if err != nil {
		t.Fatal(err)
	}
	details, err = e.service.CaseDetails(ctx, legacyID)
	if err != nil || details.Offer.Name != "Изменено" {
		t.Fatal(details, err)
	}
}
