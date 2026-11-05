package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestModerationMetricsCommittedDecisions(t *testing.T) {
	ctx := context.Background()
	registry := prometheus.NewRegistry()
	metrics := NewModerationMetrics(registry)
	store := fake.NewStore()
	clock := fake.NewClock(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	store.SeedParticipant(participant.State{TenantID: "t1", Version: 1, Trust: participant.Probation})
	store.SeedProduct(catalog.ProductState{
		TenantID:  "t1",
		ProductID: "p1",
		Revision:  1,
		Version:   1,
		Card:      catalog.Card{Name: "Товар", Marketplace: &taxonomy.Card{CategoryID: "c1"}},
	})
	service := appmoderation.NewService(store, clock, &fake.IDs{}, appmoderation.Config{
		ProbationOffers: 50,
		Metrics:         metrics,
	})
	offer := ids.OfferID{Tenant: "t1", Product: "p1"}
	store.FailCommit = errors.New("ошибка фиксации")
	if _, err := service.OpenCase(ctx, offer, "Проверка", moderation.Actor{}); err == nil {
		t.Fatal("ошибка фиксации потеряна")
	}
	if value := moderationMetric(t, registry, "marketplace_moderation_verdicts_total"); value != 0 {
		t.Fatal("откат учтён в счётчике", value)
	}
	store.FailCommit = nil
	c, err := service.OpenCase(ctx, offer, "Проверка", moderation.Actor{})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if err := metrics.Refresh(ctx, service, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if value := moderationMetric(t, registry, "marketplace_moderation_cases_open"); value != 1 {
		t.Fatal(value)
	}
	if value := moderationMetric(t, registry, "marketplace_moderation_case_oldest_age_seconds"); value != 60 {
		t.Fatal(value)
	}
	cmd := appmoderation.DecisionCommand{
		CaseID:          c.ID,
		ExpectedVersion: c.Version,
		Note:            "Проверено",
		Operator:        moderation.Actor{ID: "o1", Name: "Оператор"},
		IdempotencyKey:  "k1",
	}
	for range 2 {
		if _, err := service.Allow(ctx, cmd); err != nil {
			t.Fatal(err)
		}
	}
	if value := moderationMetric(t, registry, "marketplace_moderation_decisions_total"); value != 1 {
		t.Fatal(value)
	}
	if value := moderationMetric(t, registry, "marketplace_moderation_verdicts_total"); value != 2 {
		t.Fatal(value)
	}
	if err := metrics.Refresh(ctx, service, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if value := moderationMetric(t, registry, "marketplace_moderation_cases_open"); value != 0 {
		t.Fatal(value)
	}
	if value := moderationMetric(t, registry, "marketplace_moderation_case_oldest_age_seconds"); value != 0 {
		t.Fatal(value)
	}
}

func moderationMetric(t *testing.T, registry *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	value := 0.0
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			value += metric.GetCounter().GetValue() + metric.GetGauge().GetValue()
		}
	}
	return value
}
