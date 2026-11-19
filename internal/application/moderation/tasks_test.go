package moderation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestReevaluationTaskYieldsAndContinuesOnePage(t *testing.T) {
	e := setup()
	for n := range 205 {
		e.project(t, ids.ProductID(fmt.Sprintf("p%03d", n)), 1)
	}
	if err := e.service.AddStopPhrase(context.Background(), "Товар", moderation.Actor{}); err != nil {
		t.Fatal(err)
	}
	initial := appmoderation.ReevaluateTask("t1", e.clock.Now())
	task := ports.Task{ID: 10000, Kind: initial.Kind, TenantID: initial.TenantID, Payload: initial.Payload}
	if err := e.service.HandleTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if len(e.view(t, "p199").Verdict.Reasons) != 2 || len(e.view(t, "p200").Verdict.Reasons) != 1 {
		t.Fatal("первая задача должна переоценить ровно 200 предложений")
	}
	found := false
	for _, row := range e.store.OutboxRows() {
		var payload struct {
			ProductAfter string `json:"productAfter"`
		}
		decoded := json.Unmarshal(row.Payload, &payload) == nil
		if row.Kind == appmoderation.TaskReevaluate && decoded && payload.ProductAfter != "" {
			task = ports.Task{ID: row.ID, Kind: row.Kind, TenantID: row.TenantID, Payload: row.Payload}
			found = true
		}
	}
	if !found {
		t.Fatal("нет сохранённого продолжения")
	}
	if err := e.service.HandleTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if len(e.view(t, "p204").Verdict.Reasons) != 2 {
		t.Fatal("продолжение пропустило хвост магазина")
	}
}
