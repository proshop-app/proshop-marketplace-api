package moderation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

func TestM5QueueSummariesAndFilters(t *testing.T) {
	e := setup()
	ctx := context.Background()
	e.project(t, "p1", 1)
	e.clock.Advance(time.Minute)
	e.project(t, "p2", 1)
	for _, order := range []string{"oldest", "newest"} {
		page, err := e.service.ListCases(ctx, moderation.Filter{Order: order, Reason: "moderation.probation"})
		if err != nil || page.Total != 2 {
			t.Fatal(page, err)
		}
		first := page.Items[0]
		if (order == "oldest" && first.ProductID != "p1") || (order == "newest" && first.ProductID != "p2") {
			t.Fatal(page)
		}
		if first.Card.Name != "Товар" || first.Card.Price != 0 || first.Card.Seller.Name != "Магазин" {
			t.Fatal(first.Card)
		}
	}
	page, err := e.service.ListCases(ctx, moderation.Filter{Reason: "moderation.image_missing"})
	if err != nil || page.Total != 0 {
		t.Fatal(page, err)
	}
	verdicts, err := e.service.ListVerdicts(ctx, moderation.Filter{DecidedBy: moderation.Rule})
	if err != nil || verdicts.Total != 2 || verdicts.Items[0].Card.Name != "Товар" {
		t.Fatal(verdicts, err)
	}
	verdicts, err = e.service.ListVerdicts(ctx, moderation.Filter{DecidedBy: moderation.Operator})
	if err != nil || verdicts.Total != 0 {
		t.Fatal(verdicts, err)
	}
}

func TestM5VerdictHistory(t *testing.T) {
	e := setup()
	ctx := context.Background()
	e.project(t, "p1", 1)
	c := e.view(t, "p1").Case
	if err := e.service.AddReason(ctx, moderation.ReasonCode{Code: "r1", Text: "Текст"}); err != nil {
		t.Fatal(err)
	}
	_, err := e.service.Reject(ctx, appmoderation.DecisionCommand{
		CaseID:          c.ID,
		ExpectedVersion: c.Version,
		Operator:        moderation.Actor{ID: "o1", Name: "Оператор"},
		Reason:          "r1",
		Note:            "Заметка",
	})
	if err != nil {
		t.Fatal(err)
	}
	view := e.view(t, "p1")
	if len(view.Verdicts) != 2 || view.Verdict.Operator.ID != "o1" ||
		view.Verdict.Decision.Reason.Text != "Текст" || view.Verdict.Reasons[0].Message != "Текст" {
		t.Fatal(view)
	}
	if view.Offer.Name != "Товар" || !view.Participant.Visible {
		t.Fatal(view)
	}
	e.project(t, "p1", 2)
	view = e.view(t, "p1")
	if len(view.Verdicts) != 3 || view.Verdicts[0].Revision != 2 ||
		view.Verdicts[0].RulesRevision != 1 || view.Verdicts[0].TaxonomyVersion != 1 ||
		view.Verdicts[0].Decision != nil || view.Verdicts[1].Operator.ID != "o1" {
		t.Fatal(view.Verdicts)
	}
	rollback := errors.New("откат")
	err = e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		v, err := tx.ModerationVerdicts().Get(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		v.Decide(moderation.Allow, "", e.clock.Now())
		if err := tx.ModerationVerdicts().Save(ctx, v); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) || len(e.view(t, "p1").Verdicts) != 3 {
		t.Fatal(err)
	}
	for revision := int64(3); revision <= 55; revision++ {
		e.project(t, "p1", revision)
	}
	view = e.view(t, "p1")
	if len(view.Verdicts) != 50 || view.Verdicts[0].Revision != 55 {
		t.Fatal(view.Verdicts)
	}
	rules, err := e.service.Rules(ctx)
	if err != nil || rules.Revision != 1 || rules.TaxonomyVersion != 1 ||
		!rules.RequireImage || rules.ProbationOffers != 50 {
		t.Fatal(rules, err)
	}
}

func TestM5ReevaluationAndSnapshot(t *testing.T) {
	e := setup()
	ctx := context.Background()
	e.project(t, "p1", 1)
	if err := e.service.Reevaluate(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if len(e.view(t, "p1").Verdicts) != 1 {
		t.Fatal("повторная оценка создала запись")
	}
	if err := e.service.AddStopPhrase(ctx, "Товар", moderation.Actor{}); err != nil {
		t.Fatal(err)
	}
	if err := e.service.Reevaluate(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	view := e.view(t, "p1")
	if len(view.Verdicts) != 2 || view.Verdicts[0].RulesRevision != 2 {
		t.Fatal(view.Verdicts)
	}
	c := view.Case
	if _, err := e.service.Allow(ctx, appmoderation.DecisionCommand{
		CaseID:          c.ID,
		ExpectedVersion: c.Version,
		Note:            "Заметка",
	}); err != nil {
		t.Fatal(err)
	}
	product, _ := e.store.Product("t1", "p1")
	product.Card.Name = "Изменено"
	e.store.SeedProduct(product)
	cases, err := e.service.ListCases(ctx, moderation.Filter{Status: moderation.Allowed})
	if err != nil || cases.Items[0].Card.Name != "Товар" {
		t.Fatal(cases, err)
	}
	verdicts, err := e.service.ListVerdicts(ctx, moderation.Filter{DecidedBy: moderation.Operator})
	if err != nil || verdicts.Items[0].Card.Name != "Изменено" {
		t.Fatal(verdicts, err)
	}
	member, _ := e.store.Participant("t1")
	member.Stage = "disconnected"
	member.Enabled = false
	e.store.SeedParticipant(member)
	if e.view(t, "p1").Participant.Visible {
		t.Fatal("видимость не учитывает согласие магазина")
	}
}
