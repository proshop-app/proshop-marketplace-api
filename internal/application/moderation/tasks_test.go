package moderation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
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

func TestReevaluationQueuesStoreCardOnlyWhenVisibilityChanges(t *testing.T) {
	ctx := context.Background()
	e := setup()
	member, _ := e.store.Participant("t1")
	member.Trust = participant.Trusted
	e.store.SeedParticipant(member)
	e.project(t, "p1", 1)
	e.project(t, "p2", 1)
	index := appcatalog.NewIndexer(e.store, fake.NewIndex(), e.clock, 50, nil, nil, 0)
	request := ports.ClaimRequest{
		Now:        e.clock.Now(),
		Limit:      10,
		PerTenant:  10,
		LeaseUntil: e.clock.Now().Add(time.Minute),
	}
	claim := func(kind string) []ports.Task {
		t.Helper()
		request.Kinds = []string{kind}
		tasks, err := e.store.Claim(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		return tasks.Tasks
	}
	complete := func(tasks []ports.Task) {
		t.Helper()
		for _, task := range tasks {
			if err := e.store.Complete(ctx, task.ID, task.Attempts, e.clock.Now()); err != nil {
				t.Fatal(err)
			}
		}
	}
	initial := claim(appcatalog.TaskSyncOffer)
	for _, err := range index.HandleOfferBatch(ctx, initial) {
		if err != nil {
			t.Fatal(err)
		}
	}
	complete(initial)
	complete(claim(ports.TaskStoreCard))
	for i, phrase := range []string{"Иное", "Товар"} {
		if err := e.service.AddStopPhrase(ctx, phrase, moderation.Actor{}); err != nil {
			t.Fatal(err)
		}
		if err := e.service.Reevaluate(ctx, "t1"); err != nil {
			t.Fatal(err)
		}
		offers := claim(appcatalog.TaskSyncOffer)
		for _, err := range index.HandleOfferBatch(ctx, offers) {
			if err != nil {
				t.Fatal(err)
			}
		}
		complete(offers)
		if tasks := claim(ports.TaskStoreCard); len(tasks) != i {
			t.Fatalf("сводка после фразы %q: %d задач", phrase, len(tasks))
		}
	}
}
