package participant_test

import (
	"context"
	"testing"

	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
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
	if err != nil || page.Total != 3 {
		t.Fatal(page, err)
	}
	for _, record := range page.Items {
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
