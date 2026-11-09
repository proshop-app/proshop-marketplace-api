package moderation_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type environment struct {
	store     *fake.Store
	clock     *fake.Clock
	service   *appmoderation.Service
	projector *appcatalog.Projector
}

func setup() environment {
	store := fake.NewStore()
	clock := fake.NewClock(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	store.SeedParticipant(participant.State{
		Delivery: participant.DeliverySettings{Regions: []string{"RU-AD"}, Carriers: []string{"cdek"}},
		Subsidy:  participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
		TenantID: "t1",
		Slug:     "t1",
		Name:     "Магазин",
		Stage:    participant.StageConnected,
		Enabled:  true,
		Trust:    participant.Probation,
		Version:  1,
	})
	store.SeedTenant(participant.TenantState{TenantID: "t1", Slug: "t1", Status: participant.TenantActive, Version: 1})
	store.SeedTaxonomy(taxonomy.Definition{
		Version:    1,
		Categories: []taxonomy.Category{{ID: "c1", Name: "Категория", Slug: "c1"}},
	})
	service := appmoderation.NewService(store, clock, &fake.IDs{}, appmoderation.Config{
		ProbationOffers: 50,
		RequireImage:    true,
	})
	return environment{
		store:     store,
		clock:     clock,
		service:   service,
		projector: appcatalog.NewProjector(store, clock, service),
	}
}

func (e environment) project(t *testing.T, product ids.ProductID, revision int64) {
	t.Helper()
	snapshot := catalog.Snapshot{
		TenantID:  "t1",
		ProductID: product,
		Revision:  revision,
		At:        e.clock.Now(),
		Card: catalog.Card{
			Name:        "Товар",
			Image:       "image",
			Visible:     true,
			Marketplace: &taxonomy.Card{CategoryID: "c1", Published: true},
		},
		PriceProfiles: []catalog.PriceProfile{{Key: catalog.DefaultPriceProfile, Price: money.FromKopecks(100)}},
	}
	err := e.projector.ApplyProduct(context.Background(), appcatalog.ProductEvent{
		EventID:  fmt.Sprintf("%s:%d", product, revision),
		Snapshot: snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e environment) view(t *testing.T, product ids.ProductID) appmoderation.OfferView {
	t.Helper()
	view, err := e.service.GetOffer(context.Background(), ids.OfferID{Tenant: "t1", Product: product})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func (e environment) document(t *testing.T) ports.OfferDocument {
	t.Helper()
	var doc ports.OfferDocument
	err := e.store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		state, err := tx.Offers().Offer(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		doc, _ = appcatalog.Document(state)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestProjectionAndDecision(t *testing.T) {
	e := setup()
	e.project(t, "p1", 1)
	view := e.view(t, "p1")
	if view.Verdict.Status != moderation.Pending || view.Case == nil || e.document(t).Name != "" {
		t.Fatal("испытание должно скрывать карточку и открывать дело")
	}
	cmd := appmoderation.DecisionCommand{CaseID: view.Case.ID, ExpectedVersion: view.Case.Version, Note: "Проверено"}
	cmd.ExpectedVersion++
	if _, err := e.service.Allow(context.Background(), cmd); !errors.Is(err, moderation.ErrCaseConflict) {
		t.Fatal(err)
	}
	cmd.ExpectedVersion--
	if _, err := e.service.Allow(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if !e.document(t).Visible || e.document(t).Name != "Товар" {
		t.Fatal(e.document(t))
	}
	if _, err := e.service.Allow(context.Background(), cmd); !errors.Is(err, moderation.ErrCaseConflict) {
		t.Fatal(err)
	}
	if err := e.service.AddStopPhrase(context.Background(), "Товар", moderation.Actor{}); err != nil {
		t.Fatal(err)
	}
	if err := e.service.Reevaluate(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if !e.document(t).Visible {
		t.Fatal("переоценка затёрла ручное одобрение")
	}
	e.project(t, "p1", 2)
	if e.document(t).Visible || e.view(t, "p1").Case == nil {
		t.Fatal("новая ревизия должна пройти правила")
	}
}

func TestFiftiethApprovalAndReevaluation(t *testing.T) {
	e := setup()
	ctx := context.Background()
	e.project(t, "p1", 1)
	for i := 2; i <= 51; i++ {
		id := ids.ProductID(fmt.Sprintf("p%d", i))
		e.project(t, id, 1)
		c := e.view(t, id).Case
		_, err := e.service.Allow(ctx, appmoderation.DecisionCommand{
			CaseID:          c.ID,
			ExpectedVersion: c.Version,
			Note:            "Проверено",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	p, _ := e.store.Participant("t1")
	statusTask(t, e, moderationstatus.TaskParticipant)
	if p.Trust != participant.Trusted || p.ApprovedOffers != 50 {
		t.Fatal(p)
	}
	queued := false
	for _, task := range e.store.OutboxRows() {
		if task.Kind == appmoderation.TaskReevaluate && task.TenantID == "t1" {
			queued = true
		}
	}
	if !queued {
		t.Fatal("завершение испытания не поставило переоценку")
	}
	members := appparticipant.NewService(e.store, e.clock, &fake.IDs{})
	journal, err := members.ListDecisions(ctx, "t1", participant.Page{Limit: 10})
	if err != nil || journal.Total != 1 || journal.Items[0].Reason != "" ||
		journal.Items[0].Actor != "system" || journal.Items[0].Note != "Одобрено 50 предложений" {
		t.Fatal(journal, err)
	}
	publisher := &fake.Publisher{}
	status := moderationstatus.NewService(e.store, e.clock, publisher, "offer", "participant")
	if err := status.HandleTask(ctx, statusTask(t, e, moderationstatus.TaskParticipant)); err != nil {
		t.Fatal(err)
	}
	var event moderationstatus.ParticipantEvent
	if err := json.Unmarshal(publisher.Messages()[0].Value, &event); err != nil {
		t.Fatal(err)
	}
	if event.By != "system" || event.Reason != nil || event.Note != "Одобрено 50 предложений" {
		t.Fatal(event)
	}
	if err := e.service.Reevaluate(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if !e.document(t).Visible || e.view(t, "p1").Case != nil {
		t.Fatal("дело не закрыто переоценкой")
	}
	cases, err := e.service.ListCases(ctx, moderation.Filter{Status: "superseded"})
	if err != nil || cases.Total != 1 {
		t.Fatal(cases, err)
	}
	if err := e.service.AddStopPhrase(ctx, "Товар", moderation.Actor{}); err != nil {
		t.Fatal(err)
	}
	if err := e.service.Reevaluate(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if e.document(t).Visible {
		t.Fatal("стоп-фраза не скрыла предложение")
	}
}

func TestStaleAndRollback(t *testing.T) {
	e := setup()
	ctx := context.Background()
	e.project(t, "p1", 1)
	c := e.view(t, "p1").Case
	err := e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		product, err := tx.Products().Get(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		state := product.State()
		next := catalog.Snapshot{
			TenantID:      state.TenantID,
			ProductID:     state.ProductID,
			Revision:      state.Revision + 1,
			Card:          state.Card,
			PriceProfiles: state.PriceProfiles,
			At:            e.clock.Now(),
		}
		_, err = product.Apply(next, e.clock.Now())
		if err != nil {
			return err
		}
		return tx.Products().Save(ctx, product)
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := appmoderation.DecisionCommand{CaseID: c.ID, ExpectedVersion: c.Version, Note: "Проверено"}
	if _, err := e.service.Allow(ctx, cmd); !errors.Is(err, moderation.ErrCaseStale) {
		t.Fatal(err)
	}
	e.store.FailCommit = errors.New("сбой фиксации")
	if err := e.service.AddStopPhrase(ctx, "Товар", moderation.Actor{}); err == nil {
		t.Fatal("ожидался откат")
	}
	e.store.FailCommit = nil
	phrases, err := e.service.ListStopPhrases(ctx)
	if err != nil || len(phrases) != 0 {
		t.Fatal(phrases, err)
	}
}

func TestTaxonomyAndMissingVerdict(t *testing.T) {
	e := setup()
	e.project(t, "p1", 1)
	ctx := context.Background()
	service := apptaxonomy.NewService(e.store, e.clock, e.service, 0)
	// Поставка проходит полную проверку набора: листу нужна схема, схеме — атрибут.
	definition := taxonomy.Definition{
		Version:    2,
		Attributes: []taxonomy.Attribute{{Code: "a1", Name: "Атрибут", Type: "string"}},
		Schemas:    map[string]taxonomy.Schema{"s1": {Attributes: []string{"a1"}, Optional: []string{"a1"}}},
		Categories: []taxonomy.Category{
			{ID: "c1", Name: "Категория", Slug: "c1", Schema: "s1", Moderation: "forbidden"},
		},
	}
	body, _ := json.Marshal(definition)
	checksum := sha256.Sum256(body)
	_, err := service.Apply(ctx, taxonomy.Release{
		Version:         definition.Version,
		Checksum:        hex.EncodeToString(checksum[:]),
		Attributes:      definition.Attributes,
		Schemas:         definition.Schemas,
		Categories:      definition.Categories,
		ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.HandleTask(ctx, ports.Task{
		Kind:    apptaxonomy.TaskRevalidate,
		Payload: []byte(`{"version":2}`),
	}); err != nil {
		t.Fatal(err)
	}
	if e.view(t, "p1").Verdict.Status != moderation.Rejected {
		t.Fatal("политика не применена")
	}
	err = e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		state, err := tx.Offers().Offer(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		state.Verdict = nil
		doc, ok := appcatalog.Document(state)
		if !ok || doc.Visible || doc.Name != "" {
			t.Fatal(doc)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNoCardAndDeletedAreNotEvaluated(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		e := setup()
		ctx := context.Background()
		err := e.projector.ApplyProduct(ctx, appcatalog.ProductEvent{
			EventID: "event1",
			Snapshot: catalog.Snapshot{
				TenantID:  "t1",
				ProductID: "p1",
				Revision:  1,
				Deleted:   deleted,
				At:        e.clock.Now(),
				Card:      catalog.Card{Name: "Товар", Visible: true},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		view := e.view(t, "p1")
		if view.Verdict != nil || view.Case != nil {
			t.Fatal("структурно скрытый снимок получил оценку")
		}
	}
}

func TestOperatorCaseRejectAndNewRevision(t *testing.T) {
	e := setup()
	ctx := context.Background()
	members := appparticipant.NewService(e.store, e.clock, &fake.IDs{})
	if _, err := members.SetTrust(ctx, "t1", participant.Trusted, participant.Decision{Note: "Заметка"}); err != nil {
		t.Fatal(err)
	}
	e.project(t, "p1", 1)
	if !e.document(t).Visible {
		t.Fatal("чистая карточка скрыта")
	}
	initial := statusTask(t, e, moderationstatus.TaskOffer)
	if err := e.store.Complete(ctx, initial.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	c, err := e.service.OpenCase(ctx, ids.OfferID{Tenant: "t1", Product: "p1"}, "Проверка", moderation.Actor{})
	if err != nil {
		t.Fatal(err)
	}
	task := statusTask(t, e, moderationstatus.TaskOffer)
	if err := e.store.Complete(ctx, task.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if e.document(t).Visible || c.Source != moderation.Operator {
		t.Fatal(c)
	}
	_, err = e.service.Reject(ctx, appmoderation.DecisionCommand{
		CaseID:          c.ID,
		ExpectedVersion: c.Version,
		Note:            "Отклонено",
	})
	if err != nil {
		t.Fatal(err)
	}
	statusTask(t, e, moderationstatus.TaskOffer)
	rejected := e.view(t, "p1")
	if rejected.Verdict.Status != moderation.Rejected || len(rejected.History) != 1 {
		t.Fatal(rejected)
	}
	e.project(t, "p1", 2)
	pending := e.view(t, "p1")
	if pending.Verdict.Status != moderation.Pending || pending.Case == nil ||
		pending.Verdict.Reasons[0].Key != "moderation.rejected_revision" {
		t.Fatal(pending)
	}
}

func TestConcurrentIdempotentDecisions(t *testing.T) {
	e := setup()
	e.project(t, "p1", 1)
	c := e.view(t, "p1").Case
	cmd := appmoderation.DecisionCommand{
		CaseID:          c.ID,
		ExpectedVersion: c.Version,
		Operator:        moderation.Actor{ID: "o1", Name: "Оператор"},
		Note:            "Проверено",
		IdempotencyKey:  "k1",
	}
	results := make(chan error, 8)
	for range 8 {
		go func() {
			_, err := e.service.Allow(context.Background(), cmd)
			results <- err
		}()
	}
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	view := e.view(t, "p1")
	if len(view.History) != 1 {
		t.Fatal("повтор создал запись журнала", view.History)
	}
	if _, err := e.service.Reject(context.Background(), cmd); !errors.Is(err, moderation.ErrKeyReused) {
		t.Fatal(err)
	}
	cmd.Operator.ID = "o2"
	if _, err := e.service.Allow(context.Background(), cmd); !errors.Is(err, moderation.ErrCaseConflict) {
		t.Fatal("ключ другого оператора должен проверить версию дела", err)
	}
}
