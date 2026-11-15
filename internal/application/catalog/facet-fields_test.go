package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestFacetScopeIntersectionAndFilterUnion(t *testing.T) {
	d := taxonomy.Definition{
		Attributes: []taxonomy.Attribute{
			{Code: "a1", Type: "string", Facet: true},
			{Code: "a2", Type: "number", Facet: true},
			{Code: "a3", Type: "boolean", Facet: true},
			{Code: "a4", Type: "string", Facet: true, Deprecated: true},
			{Code: "a5", Type: "string"},
		},
		Schemas: map[string]taxonomy.Schema{
			"s1": {Attributes: []string{"a1", "a2", "a4", "a5"}},
			"s2": {Attributes: []string{"a1", "a3", "a4", "a5"}},
		},
		Categories: []taxonomy.Category{{
			ID: "c1",
			Children: []taxonomy.Category{
				{
					ID: "c2",
					Children: []taxonomy.Category{
						{ID: "c3", Schema: "s1"},
						{ID: "c4", Schema: "s1"},
					},
				},
				{ID: "c5", Schema: "s2"},
			},
		}},
	}
	store := fake.NewStore()
	store.SeedTaxonomy(d)
	queries := appcatalog.NewQueries(store, fake.NewIndex(), nil, nil, nil, fake.NewClock(time.Time{}))
	for id, want := range map[string][]string{
		"":   {"a1"},
		"c1": {"a1"},
		"c2": {"a1", "a2"},
		"c3": {"a1", "a2"},
		"c5": {"a1", "a3"},
	} {
		prepared, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{CategoryID: id})
		if err != nil {
			t.Fatal(err)
		}
		codes := []string{}
		for _, field := range prepared.FacetFields {
			codes = append(codes, field.Code)
		}
		if !reflect.DeepEqual(codes, want) {
			t.Fatalf("%q: %v, want %v", id, codes, want)
		}
		again, err := queries.PrepareQuery(context.Background(), prepared)
		if err != nil || !reflect.DeepEqual(again.FacetFields, prepared.FacetFields) {
			t.Fatalf("повторная подготовка изменила фасеты: %v", err)
		}
	}
	prepared, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{
		CategoryID: "c1",
		Attributes: map[string]ports.AttributeFilter{
			"a2": {Min: "1"},
		},
	})
	if err != nil || prepared.Attributes["a2"].Type != "number" || len(prepared.FacetFields) != 1 {
		t.Fatalf("пересечение фасетов сузило разрешённые фильтры: %+v %v", prepared, err)
	}
}

func TestFacetBudgetAndSearchFilterLimit(t *testing.T) {
	d := taxonomy.Definition{}
	for i := 0; i <= ports.MaxSearchAttributes; i++ {
		d.Attributes = append(d.Attributes, taxonomy.Attribute{
			Code:  fmt.Sprintf("a%03d", i),
			Type:  "string",
			Facet: true,
		})
	}
	// Допустимая схема с числом полей больше бюджета панели.
	codes := []string{}
	for _, field := range d.Attributes[:ports.MaxSearchAttributes] {
		codes = append(codes, field.Code)
	}
	d.Schemas = map[string]taxonomy.Schema{
		"s1": {Attributes: codes},
	}
	d.Categories = []taxonomy.Category{{ID: "c1", Schema: "s1"}}
	store := fake.NewStore()
	store.SeedTaxonomy(d)
	queries := appcatalog.NewQueries(store, fake.NewIndex(), nil, nil, nil, fake.NewClock(time.Time{}))
	filters := map[string]ports.AttributeFilter{"a255": {Values: []string{"v1"}}}
	for range 3 {
		prepared, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{Attributes: filters})
		if err != nil || len(prepared.FacetFields) != appcatalog.MaxAttributeFacets {
			t.Fatalf("бюджет фасетов: %d %v", len(prepared.FacetFields), err)
		}
		for i, field := range prepared.FacetFields {
			want := fmt.Sprintf("a%03d", i)
			if i == appcatalog.MaxAttributeFacets-1 {
				want = "a255"
			}
			if field.Code != want {
				t.Fatalf("недетерминированный выбор: %s вместо %s", field.Code, want)
			}
		}
	}
	for _, field := range d.Attributes {
		filters[field.Code] = ports.AttributeFilter{Values: []string{"v1"}}
	}
	_, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{Attributes: filters})
	if !errors.Is(err, appcatalog.ErrInvalidQuery) {
		t.Fatalf("более 256 фильтров переданы поиску: %v", err)
	}
	delete(filters, "a256")
	if _, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{Attributes: filters}); err != nil {
		t.Fatalf("256 фильтров запрещены: %v", err)
	}
}
