package moderation_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (e environment) publish(t *testing.T, revision int64, published bool, category string) {
	t.Helper()
	err := e.projector.ApplyProduct(context.Background(), appcatalog.ProductEvent{
		Snapshot: catalog.Snapshot{
			TenantID:  "t1",
			ProductID: "p1",
			Revision:  revision,
			At:        e.clock.Now(),
			Card: catalog.Card{
				Name:        "Товар",
				Image:       "image",
				Marketplace: &taxonomy.Card{CategoryID: category, Published: published},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublicationEntry(t *testing.T) {
	for _, card := range []taxonomy.Card{
		{CategoryID: "c1", Published: false},
		{CategoryID: "missing", Published: true},
	} {
		e := setup()
		e.publish(t, 1, card.Published, card.CategoryID)
		view := e.view(t, "p1")
		if view.Case != nil || view.Verdict != nil {
			t.Fatal(view)
		}
		if _, err := e.service.OpenCase(
			context.Background(),
			ids.OfferID{Tenant: "t1", Product: "p1"},
			"Проверка",
			moderation.Actor{},
		); !errors.Is(err, moderation.ErrInvalid) {
			t.Fatal(err)
		}
		e.publish(t, 2, true, "c1")
		if e.view(t, "p1").Case == nil {
			t.Fatal("публикация новой ревизии не открыла дело")
		}
	}
}

func TestPublicationTaxonomyReturn(t *testing.T) {
	e := setup()
	e.store.SeedTaxonomy(taxonomy.Definition{
		Version:    1,
		Categories: []taxonomy.Category{{ID: "c1", Name: "Категория", Slug: "c1", Schema: "s1"}},
		Attributes: []taxonomy.Attribute{{Code: "a1", Name: "Свойство", Type: "string"}},
		Schemas: map[string]taxonomy.Schema{
			"s1": {Attributes: []string{"a1"}, Optional: []string{"a1"}},
		},
	})
	e.publish(t, 1, true, "c2")
	service := apptaxonomy.NewService(e.store, e.clock, e.service)
	_, err := service.LoadPart(
		context.Background(),
		"categories",
		taxonomy.Definition{
			Categories: []taxonomy.Category{
				{ID: "c1", Name: "Категория", Slug: "c1", Schema: "s1"},
				{ID: "c2", Name: "Другая", Slug: "c2", Schema: "s1"},
			},
		},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	revalidateTaxonomy(t, service)
	view := e.view(t, "p1")
	if view.Case == nil || view.Verdict.Revision != 1 || view.Verdict.TaxonomyVersion != 2 {
		t.Fatal(view)
	}
	_, err = service.LoadPartWithOptions(
		context.Background(),
		"categories",
		taxonomy.Definition{
			Categories: []taxonomy.Category{{ID: "c1", Name: "Категория", Slug: "c1", Schema: "s1"}},
		},
		2,
		apptaxonomy.LoadOptions{ReplaceCategories: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	revalidateTaxonomy(t, service)
	assertClosedPublication(t, e, view.Case.ID)
}

func assertClosedPublication(t *testing.T, e environment, id ids.UUID) {
	t.Helper()
	view := e.view(t, "p1")
	closed, err := e.service.GetCase(context.Background(), id)
	if err != nil || closed.Status != moderation.Superseded || view.Case != nil || len(view.History) != 0 {
		t.Fatal(closed, view, err)
	}
	err = e.store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		stats, err := tx.ModerationCases().OpenStats(ctx)
		if err == nil && len(stats) != 0 {
			t.Fatal(stats)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublicationClosesCase(t *testing.T) {
	e := setup()
	e.publish(t, 1, true, "c1")
	view := e.view(t, "p1")
	e.publish(t, 2, false, "c1")
	assertClosedPublication(t, e, view.Case.ID)
	if !samePublicationVerdict(view.Verdict, e.view(t, "p1").Verdict) {
		t.Fatal("снятие публикации изменило вердикт")
	}
	e.publish(t, 3, true, "c1")
	if c := e.view(t, "p1").Case; c == nil || c.ID == view.Case.ID {
		t.Fatal(c)
	}
}

func TestPublicationPreservesDecision(t *testing.T) {
	for _, action := range []moderation.Action{moderation.Allow, moderation.Reject} {
		e := setup()
		e.publish(t, 1, true, "c1")
		c := e.view(t, "p1").Case
		cmd := appmoderation.DecisionCommand{CaseID: c.ID, ExpectedVersion: c.Version, Note: "Проверено"}
		decide := e.service.Allow
		if action == moderation.Reject {
			decide = e.service.Reject
		}
		if _, err := decide(context.Background(), cmd); err != nil {
			t.Fatal(err)
		}
		before := e.view(t, "p1").Verdict
		for _, published := range []bool{false, true} {
			e.seedPublication(t, published)
			if err := e.service.Reevaluate(context.Background(), "t1"); err != nil {
				t.Fatal(err)
			}
			view := e.view(t, "p1")
			if !samePublicationVerdict(before, view.Verdict) || view.Case != nil || len(view.History) != 1 {
				t.Fatal("переоценка текущей ревизии изменила решение", view)
			}
		}
		e.publish(t, 2, false, "c1")
		if !samePublicationVerdict(before, e.view(t, "p1").Verdict) {
			t.Fatal("потеряно решение")
		}
		e.publish(t, 3, true, "c1")
		if action == moderation.Reject {
			view := e.view(t, "p1")
			found := false
			for _, reason := range view.Verdict.Reasons {
				found = found || reason.Key == "moderation.rejected_revision"
			}
			if !found || view.Case == nil || len(view.History) != 1 {
				t.Fatal(view)
			}
		}
	}
}

func (e environment) seedPublication(t *testing.T, published bool) {
	t.Helper()
	err := e.store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Products().Get(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		state := p.State()
		state.Card.Publication.Published = published
		if !published {
			state.Card.Publication.Issues = []taxonomy.Issue{{Code: "taxonomy.unpublished"}}
		} else {
			state.Card.Publication.Issues = nil
		}
		return tx.Products().Save(ctx, catalog.RestoreProduct(state))
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublicationReevaluationAndStaleDecision(t *testing.T) {
	e := setup()
	e.publish(t, 1, true, "c1")
	before := e.view(t, "p1")
	e.seedPublication(t, false)
	cmd := appmoderation.DecisionCommand{
		CaseID:          before.Case.ID,
		ExpectedVersion: before.Case.Version,
		Note:            "Проверено",
	}
	if _, err := e.service.Allow(context.Background(), cmd); !errors.Is(err, moderation.ErrCaseStale) {
		t.Fatal(err)
	}
	if err := e.service.Reevaluate(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	assertClosedPublication(t, e, before.Case.ID)
	e.seedPublication(t, true)
	if err := e.service.Reevaluate(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	after := e.view(t, "p1")
	if after.Case == nil || after.Case.ID == before.Case.ID || !samePublicationVerdict(before.Verdict, after.Verdict) {
		t.Fatal(after)
	}
}

func samePublicationVerdict(before, after *moderation.VerdictState) bool {
	a, b := *before, *after
	a.Card, b.Card = nil, nil
	return reflect.DeepEqual(a, b)
}
