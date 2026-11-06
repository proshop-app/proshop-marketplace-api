package participant_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// candidateUOW воспроизводит проигрыш вставки и отказ одного магазина поверх fake.
type candidateUOW struct {
	ports.UnitOfWork
	fail ids.TenantID
	race bool
}

type candidateTx struct {
	ports.Tx
	fail ids.TenantID
	race bool
}

type candidateRepository struct {
	participant.Repository
	fail ids.TenantID
	race bool
}

func (u candidateUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return u.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return fn(ctx, candidateTx{Tx: tx, fail: u.fail, race: u.race})
	})
}

func (tx candidateTx) Participants() participant.Repository {
	return candidateRepository{Repository: tx.Tx.Participants(), fail: tx.fail, race: tx.race}
}

func (r candidateRepository) Add(ctx context.Context, p *participant.Participant) error {
	if p.TenantID() == r.fail {
		return participant.ErrSlugTaken
	}
	if r.race {
		if err := r.Repository.Add(ctx, p); err != nil {
			return err
		}
		// Другой создатель уже вставил участника между чтением и нашей вставкой.
		return participant.ErrAlreadyExists
	}
	return r.Repository.Add(ctx, p)
}

func TestCandidateRegistryAndBackfill(t *testing.T) {
	store := fake.NewStore()
	clock := fake.NewClock(t0)
	identities := &fake.IDs{}
	registry := appparticipant.NewRegistry(store, clock, identities)
	ctx := context.Background()
	event := registryEvent("created", "slug", participant.TenantActive, t0)
	event.Event.Name = "Имя из реестра"
	if err := registry.Apply(ctx, event); err != nil {
		t.Fatal(err)
	}
	p, ok := store.Participant("t9")
	if !ok || p.Name != event.Event.Name || p.Stage != participant.StageCandidate || p.Trust != participant.Probation {
		t.Fatal(p)
	}
	if err := registry.Apply(ctx, event); err != nil {
		t.Fatal(err)
	}
	event.EventID = "duplicate-snapshot"
	if err := registry.Apply(ctx, event); err != nil {
		t.Fatal(err)
	}
	service := appparticipant.NewService(store, clock, identities)
	history, err := service.ListDecisions(ctx, "t9", participant.Page{Limit: 10})
	if err != nil || history.Total != 1 || history.Items[0].Actor != "system" ||
		history.Items[0].Action != "candidate" {
		t.Fatal(history, err)
	}
	for _, status := range []participant.TenantStatus{participant.TenantSuspended, participant.TenantDeleted} {
		event := registryEvent(string(status), string(status), status, t0)
		event.Event.TenantID = participantID(string(status))
		if err := registry.Apply(ctx, event); err != nil {
			t.Fatal(err)
		}
		if _, ok := store.Participant(event.Event.TenantID); ok {
			t.Fatal(status)
		}
	}
	// Больше страницы: смещение не должно пропускать записи после вставок участников.
	for i := 0; i < 205; i++ {
		id := participantID(fmt.Sprintf("t%03d", i))
		store.SeedTenant(participant.TenantState{
			TenantID: id,
			Slug:     string(id),
			Status:   participant.TenantActive,
			Version:  1,
		})
	}
	syncer := appparticipant.NewSettings(store, fake.NewPlatform(), clock, identities)
	result, err := syncer.Sync(ctx)
	if err != nil || result.Checked != 206 || result.Candidates != 206 || result.Queue != 0 {
		t.Fatal(result, err)
	}
	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	history, err = service.ListDecisions(ctx, "t000", participant.Page{Limit: 10})
	if err != nil || history.Total != 1 {
		t.Fatal(history, err)
	}
	if p, _ := store.Participant("t000"); p.Name != "t000" {
		t.Fatal(p)
	}
}

func TestCandidateReviewLifecycle(t *testing.T) {
	store, clock, service := setup(t)
	ctx := context.Background()
	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: "Магазин"}); err != nil {
		t.Fatal(err)
	}
	assertQueue := func(want int) {
		t.Helper()
		list, err := service.List(ctx, participant.Page{Queue: true})
		if err != nil || list.Total != want {
			t.Fatal(list, err)
		}
	}
	assertQueue(0)
	platform := fake.NewPlatform()
	platform.SetSettings("t1", ports.ShopSettings{
		Subsidy:  participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
		Delivery: w3SettingsDelivery,
	})
	syncer := appparticipant.NewSettings(store, platform, clock, &fake.IDs{})
	if _, err := syncer.SyncOne(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	assertQueue(1)
	reasons := appmoderation.NewService(store, clock, &fake.IDs{}, appmoderation.Config{})
	if err := reasons.AddReason(ctx, moderation.ReasonCode{Code: "r1", Text: "Причина"}); err != nil {
		t.Fatal(err)
	}
	decision := participant.Decision{Reason: "r1", Note: "Исправьте сведения"}
	view, err := service.Return(ctx, "t1", decision)
	if err != nil || view.Participant.Stage != participant.StageReturned || view.Participant.LastDecision == nil {
		t.Fatal(view, err)
	}
	assertQueue(0)
	publisher := &fake.Publisher{}
	events := moderationstatus.NewService(store, clock, publisher, "offers", "participants")
	if err := events.HandleTask(ctx, taskOf(moderationstatus.ParticipantTask("t1", t0))); err != nil {
		t.Fatal(err)
	}
	var event moderationstatus.ParticipantEvent
	if err := json.Unmarshal(publisher.Messages()[0].Value, &event); err != nil {
		t.Fatal(err)
	}
	if event.Stage != participant.StageReturned || event.Enabled || event.By != "operator" || event.Note == "" ||
		event.Reason == nil || event.Reason.Code != "r1" {
		t.Fatal(event)
	}
	view, err = service.Submit(ctx, "t1")
	if err != nil || view.Participant.LastDecision.Note != "Исправьте сведения" {
		t.Fatal(view, err)
	}
	assertQueue(1)
	if _, err := service.Submit(ctx, "t1"); !errors.Is(err, participant.ErrConflict) {
		t.Fatal(err)
	}
	view, err = service.Enable(ctx, "t1", participant.Decision{})
	if err != nil || !view.Visibility.Visible || view.Participant.Stage != participant.StageConnected {
		t.Fatal(view, err)
	}
	assertQueue(0)
	view, err = service.Disable(ctx, "t1", participant.Decision{Note: "Нарушение"})
	if err != nil || view.Visibility.Visible || view.Participant.Stage != participant.StageDisconnected {
		t.Fatal(view, err)
	}
}

func TestCandidateInvalidRegistryNameFallsBack(t *testing.T) {
	for _, name := range []string{"", " ", strings.Repeat("я", 201), "Имя\nмагазина"} {
		t.Run(name, func(t *testing.T) {
			store := fake.NewStore()
			registry := appparticipant.NewRegistry(store, fake.NewClock(t0), &fake.IDs{})
			event := registryEvent("created", "slug", participant.TenantActive, t0)
			event.Event.Name = name
			if err := registry.Apply(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			p, ok := store.Participant("t9")
			if !ok || p.Name != "slug" {
				t.Fatal(p)
			}
		})
	}
}

func TestCandidateCreateRaceIsIdempotent(t *testing.T) {
	store := fake.NewStore()
	uow := candidateUOW{UnitOfWork: store, race: true}
	registry := appparticipant.NewRegistry(uow, fake.NewClock(t0), &fake.IDs{})
	event := registryEvent("created", "slug", participant.TenantActive, t0)
	if err := registry.Apply(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Participant("t9"); !ok {
		t.Fatal("участник победителя потерян")
	}
	if len(store.OutboxRows()) != 0 {
		t.Fatal("проигравший создал повторные задачи")
	}
}

func TestCandidateBackfillIsolatesFailureAndAlreadyExists(t *testing.T) {
	store, clock, service := setup(t)
	ctx := context.Background()
	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: "Магазин"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []ids.TenantID{"t2", "t3"} {
		store.SeedTenant(participant.TenantState{
			TenantID: id,
			Slug:     string(id),
			Status:   participant.TenantActive,
			Version:  1,
		})
	}
	platform := fake.NewPlatform()
	for _, id := range []ids.TenantID{"t1", "t3"} {
		platform.SetSettings(id, ports.ShopSettings{
			Delivery: w3SettingsDelivery,
			Subsidy:  participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
		})
	}
	syncer := appparticipant.NewSettings(candidateUOW{UnitOfWork: store, fail: "t2"}, platform, clock, &fake.IDs{})
	result, err := syncer.Sync(ctx)
	if err != nil || result.Checked != 2 || result.Candidates != 2 || result.Queue != 2 ||
		len(result.Failed) != 1 || !errors.Is(result.Failed["t2"], participant.ErrSlugTaken) {
		t.Fatal(result, err)
	}
	history, err := service.ListDecisions(ctx, "t1", participant.Page{Limit: 10})
	if err != nil || history.Total != 1 {
		t.Fatal(history, err)
	}
}

func TestCandidateDecisionTransitions(t *testing.T) {
	store, _, service := setup(t)
	ctx := context.Background()
	decision := participant.Decision{Note: "Проверено"}
	for _, stage := range []participant.Stage{
		participant.StageCandidate,
		participant.StageConnected,
		participant.StageReturned,
		participant.StageDisconnected,
	} {
		store.SeedParticipant(participant.State{TenantID: "t1", Slug: "shop", Stage: stage, Version: 1})
		if stage == participant.StageCandidate {
			if _, err := service.Disable(ctx, "t1", decision); !errors.Is(err, participant.ErrConflict) {
				t.Fatal(err)
			}
			continue
		}
		if _, err := service.Return(ctx, "t1", decision); !errors.Is(err, participant.ErrConflict) {
			t.Fatal(stage, err)
		}
		if stage == participant.StageReturned {
			view, err := service.Enable(ctx, "t1", decision)
			if err != nil || view.Participant.Stage != participant.StageConnected {
				t.Fatal(view, err)
			}
		}
	}
}
