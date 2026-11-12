package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
)

func TestReferenceCandidatesPagesAndFailure(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	for _, code := range []string{"b1", "b2", "b3"} {
		store.SeedReferenceDictionary(reference.DictionaryState{
			Code:       code,
			Kind:       reference.KindOpen,
			AliasKinds: reference.AliasKinds,
			Limits:     reference.DefaultLimits(),
		})
	}
	store.SeedTaxonomy(taxonomy.Definition{
		Version: 1,
		Attributes: []taxonomy.Attribute{
			{Code: "b1", Type: "string", Dictionary: "b1"},
			{Code: "b2", Type: "string", Dictionary: "b2"},
			{Code: "b3", Type: "string", Dictionary: "b3"},
		},
	})
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	service := appreference.NewService(store, fake.NewClock(now), &fake.IDs{}, nil, appreference.Config{})
	original, err := service.Create(ctx, appreference.CreateCommand{
		Dictionary: "b2",
		Name:       "Занято",
		Status:     reference.StatusConfirmed,
		Meta:       reference.Meta{Source: reference.SourceOperator},
	})
	if err != nil {
		t.Fatal(err)
	}
	search := &fake.ReferenceCandidates{
		Errors: map[string]error{"b1": errors.New("поиск недоступен")},
		Pages: map[string]map[string]ports.AttributeValuesPage{
			"b2": {
				"": {
					Items: []ports.AttributeValue{
						{Value: "Занято", Documents: 20},
						{Value: "Новое", Documents: 12, Sellers: 3},
					},
					After: "next",
				},
				"next": {Items: []ports.AttributeValue{{Value: "Другое", Documents: 7, Sellers: 2}}},
			},
			"b3": {"": {Items: []ports.AttributeValue{{Value: "Последнее", Documents: 4}}}},
		},
	}
	metrics := prometheus.NewRegistry()
	runner := jobs.NewReferenceCandidatesRunner(service, search, time.Hour, quiet, metrics)
	runner.RunOnce(ctx)
	page, err := service.List(ctx, "b2", reference.ListFilter{})
	if err != nil || len(page.Entries) != 3 {
		t.Fatalf("записи: %+v, %v", page, err)
	}
	for _, entry := range page.Entries {
		if entry.ID == original.Entry.ID {
			if entry.Occurrences != 1 || entry.Status != reference.StatusConfirmed {
				t.Fatal("владелец изменён")
			}
		}
		if entry.CanonicalName == "Новое" {
			var metadata map[string]map[string]any
			if err := json.Unmarshal(entry.Metadata, &metadata); err != nil {
				t.Fatal(err)
			}
			catalog := metadata["catalog"]
			if entry.Occurrences != 12 || entry.Status != reference.StatusCandidate ||
				entry.Source != reference.SourceCatalog {
				t.Fatalf("кандидат: %+v", entry)
			}
			if catalog["documents"] != float64(12) || catalog["sellers"] != float64(3) ||
				catalog["observedAt"] != now.Format(time.RFC3339) {
				t.Fatalf("metadata: %v", metadata)
			}
		}
	}
	last, err := service.List(ctx, "b3", reference.ListFilter{})
	if err != nil || len(last.Entries) != 1 {
		t.Fatalf("следующий словарь: %+v %v", last, err)
	}
	if gauges(t, metrics)["marketplace_reference_candidates_failures_total"] != 1 {
		t.Fatal("нет метрики сбоя")
	}
	runner.RunOnce(ctx)
	again, err := service.Get(ctx, "b2", original.Entry.ID)
	if err != nil || again.Entry.Occurrences != 1 {
		t.Fatal("повтор изменил владельца")
	}
	if len(search.Calls) != 8 {
		t.Fatalf("пагинация: %v", search.Calls)
	}
}

func TestReferenceCandidatesSkippedScopeMetric(t *testing.T) {
	store := fake.NewStore()
	store.SeedReferenceDictionary(reference.DictionaryState{
		Code:       "d1",
		Kind:       reference.KindOpen,
		AliasKinds: reference.AliasKinds,
		Limits:     reference.DefaultLimits(),
	})
	d := taxonomy.Definition{
		Version:    1,
		Attributes: []taxonomy.Attribute{{Code: "a1", Type: "string", Dictionary: "d1"}},
		Categories: []taxonomy.Category{{ID: "c1"}, {ID: "c2"}},
	}
	for i := 0; i <= ports.MaxCandidateCategories; i++ {
		id := fmt.Sprintf("c1.x%d", i)
		d.Categories[0].Children = append(d.Categories[0].Children, taxonomy.Category{ID: id})
		d.DictionaryBindings = append(d.DictionaryBindings, taxonomy.DictionaryBinding{
			CategoryID:    id,
			AttributeCode: "a1",
			Mode:          taxonomy.BindingReplace,
			Dictionaries:  []string{},
		})
	}
	store.SeedTaxonomy(d)
	search := &fake.ReferenceCandidates{
		Pages: map[string]map[string]ports.AttributeValuesPage{
			"a1": {"": {Items: []ports.AttributeValue{{Value: "Значение", Documents: 2}}}},
		},
	}
	service := appreference.NewService(store, fake.NewClock(time.Now()), &fake.IDs{}, nil, appreference.Config{})
	metrics := prometheus.NewRegistry()
	runner := jobs.NewReferenceCandidatesRunner(service, search, time.Hour, quiet, metrics)
	runner.RunOnce(context.Background())
	counters := gauges(t, metrics)
	if counters["marketplace_reference_candidates_skipped_total"] != 1 ||
		counters["marketplace_reference_candidates_failures_total"] != 0 || len(search.Queries) != 1 ||
		len(search.Queries[0].Categories) != 1 || search.Queries[0].Categories[0] != "c2" {
		t.Fatal(counters, search.Queries)
	}
}
