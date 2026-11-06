package participant_test

import (
	"context"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func TestDecisionJournalAndTrust(t *testing.T) {
	store, _, service := setup(t)
	ctx := context.Background()
	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: "Магазин"}); err != nil {
		t.Fatal(err)
	}
	decision := participant.Decision{Operator: operator.Principal{ID: "o1", Name: "Оператор"}, Note: "Проверено"}
	readyState, ok := store.Participant("t1")
	if !ok {
		t.Fatal("участник не найден")
	}
	readyState.StoreCard.Readiness.Ready = true
	store.SeedParticipant(readyState)

	if _, err := service.Enable(ctx, "t1", decision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enable(ctx, "t1", decision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Disable(ctx, "t1", decision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetTrust(ctx, "t1", participant.Trusted, decision); err != nil {
		t.Fatal(err)
	}
	page, err := service.ListDecisions(ctx, "t1", participant.Page{Limit: 10})
	if err != nil || page.Total != 4 {
		t.Fatal(page, err)
	}
	for _, record := range page.Items {
		if record.Action == "candidate" {
			continue
		}
		if record.Operator.ID != "o1" || record.Note != "Проверено" {
			t.Fatal(record)
		}
	}
	queued := false
	for _, task := range store.OutboxRows() {
		if task.Kind == appmoderation.TaskReevaluate && task.TenantID == "t1" {
			queued = true
		}
	}
	if !queued {
		t.Fatal("смена доверия не поставила переоценку")
	}
}

func TestReasonValidationAndTrustFilter(t *testing.T) {
	store, clock, service := setup(t)
	ctx := context.Background()
	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: "Магазин"}); err != nil {
		t.Fatal(err)
	}
	reasons := appmoderation.NewService(store, clock, &fake.IDs{}, appmoderation.Config{})
	decision := participant.Decision{Reason: "r1", Note: "Заметка"}
	for _, action := range []func() error{
		func() error { _, err := service.Enable(ctx, "t1", decision); return err },
		func() error { _, err := service.Disable(ctx, "t1", decision); return err },
		func() error { _, err := service.SetTrust(ctx, "t1", participant.Trusted, decision); return err },
	} {
		if err := action(); !errors.Is(err, moderation.ErrReasonUnknown) {
			t.Fatal(err)
		}
	}
	if err := reasons.AddReason(ctx, moderation.ReasonCode{Code: "r1", Text: "Текст"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetTrust(ctx, "t1", participant.Trusted, decision); err != nil {
		t.Fatal(err)
	}
	if err := reasons.PutReason(ctx, moderation.ReasonCode{Code: "r1", Text: "Изменено"}); err != nil {
		t.Fatal(err)
	}
	history, err := service.ListDecisions(ctx, "t1", participant.Page{Limit: 10})
	if err != nil || history.Total != 2 || history.Items[0].ReasonMessage != "Текст" ||
		history.Items[0].Actor != "operator" {
		t.Fatal(history, err)
	}
	for _, trust := range []string{participant.Probation, participant.Trusted} {
		list, err := service.List(ctx, participant.Page{Trust: trust, Limit: 10})
		expected := 0
		if trust == participant.Trusted {
			expected = 1
		}
		if err != nil || list.Total != expected || len(list.Items) != expected {
			t.Fatal(list, err)
		}
	}
	if _, err := service.List(ctx, participant.Page{Trust: "unknown"}); !errors.Is(err, participant.ErrInvalidTrust) {
		t.Fatal(err)
	}
}
