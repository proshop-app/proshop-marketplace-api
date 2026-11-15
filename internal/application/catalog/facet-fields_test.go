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

func TestFacetScopeCoverageAndFilterUnion(t *testing.T) {
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
		"":   {"a1", "a2", "a3"},
		"c1": {"a1", "a2", "a3"},
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
	if err != nil || prepared.Attributes["a2"].Type != "number" || len(prepared.FacetFields) != 3 {
		t.Fatalf("охват фасетов сузил разрешённые фильтры: %+v %v", prepared, err)
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

func TestFacetBudgetRanksLeafCoverageBeforeCode(t *testing.T) {
	d := taxonomy.Definition{}
	for i := 0; i < 35; i++ {
		d.Attributes = append(d.Attributes, taxonomy.Attribute{
			Code:  fmt.Sprintf("a%03d", i),
			Type:  "string",
			Facet: true,
		})
	}
	codes := []string{}
	for _, field := range d.Attributes[1:34] {
		codes = append(codes, field.Code)
	}
	d.Schemas = map[string]taxonomy.Schema{
		"s1": {Attributes: codes},
		"s2": {Attributes: []string{"a000", "a034"}},
	}
	// Одна схема у трёх листьев должна обгонять схему одного листа даже при большем коде.
	d.Categories = []taxonomy.Category{{
		ID: "c1",
		Children: []taxonomy.Category{
			{ID: "c2", Schema: "s1"},
			{ID: "c3", Schema: "s1"},
			{ID: "c4", Schema: "s1"},
			{ID: "c5", Schema: "s2"},
		},
	}}
	store := fake.NewStore()
	store.SeedTaxonomy(d)
	queries := appcatalog.NewQueries(store, fake.NewIndex(), nil, nil, nil, fake.NewClock(time.Time{}))
	for _, id := range []string{"", "c1"} {
		for _, selected := range []bool{false, true} {
			filters := map[string]ports.AttributeFilter{}
			if selected {
				filters["a034"] = ports.AttributeFilter{Values: []string{"v1"}}
			}
			for range 3 {
				prepared, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{
					CategoryID: id,
					Attributes: filters,
				})
				if err != nil || len(prepared.FacetFields) != appcatalog.MaxAttributeFacets {
					t.Fatalf("область=%q выбран=%v: бюджет фасетов: %+v, %v", id, selected, prepared, err)
				}
				for i, field := range prepared.FacetFields {
					want := fmt.Sprintf("a%03d", i+1)
					if selected && i == appcatalog.MaxAttributeFacets-1 {
						want = "a034"
					}
					if field.Code != want {
						t.Fatalf("область=%q выбран=%v: %s вместо %s", id, selected, field.Code, want)
					}
				}
			}
		}
	}
}

func TestFacetBudgetPinsRareColor(t *testing.T) {
	d := taxonomy.Definition{}
	codes := []string{}
	for i := 0; i < 40; i++ {
		code := fmt.Sprintf("a%03d", i)
		d.Attributes = append(d.Attributes, taxonomy.Attribute{
			Code:  code,
			Type:  "string",
			Facet: true,
		})
		codes = append(codes, code)
	}
	d.Attributes = append(d.Attributes, taxonomy.Attribute{Code: "color", Type: "string", Facet: true})
	d.Schemas = map[string]taxonomy.Schema{
		"s1": {Attributes: codes},
		"s2": {Attributes: []string{"color"}},
	}
	// Все 40 обычных полей имеют охват 2, цвет — только 1 из 3 листьев.
	d.Categories = []taxonomy.Category{{
		ID: "c1",
		Children: []taxonomy.Category{
			{ID: "c2", Schema: "s1"},
			{ID: "c3", Schema: "s1"},
			{ID: "c4", Schema: "s2"},
		},
	}}
	for _, color := range []taxonomy.Attribute{
		{Code: "color", Type: "string", Facet: true},
		{Code: "color", Type: "string", Facet: true, Deprecated: true},
		{Code: "color", Type: "string"},
	} {
		d.Attributes[len(d.Attributes)-1] = color
		store := fake.NewStore()
		store.SeedTaxonomy(d)
		queries := appcatalog.NewQueries(store, fake.NewIndex(), nil, nil, nil, fake.NewClock(time.Time{}))
		for _, id := range []string{"", "c1", "c2"} {
			for _, selected := range []int{0, 1, 40} {
				filters := map[string]ports.AttributeFilter{}
				want := append([]string{}, codes[:appcatalog.MaxAttributeFacets]...)
				if selected == 1 {
					filters["a039"] = ports.AttributeFilter{Values: []string{"v1"}}
					want[len(want)-1] = "a039"
				}
				if selected == 40 {
					for _, code := range codes {
						filters[code] = ports.AttributeFilter{Values: []string{"v1"}}
					}
				}
				if id != "c2" && color.Facet && !color.Deprecated {
					want = append(want[:len(want)-1], "color")
					if selected == 1 {
						want[len(want)-2] = "a039"
					}
				}
				prepared, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{
					CategoryID: id,
					Attributes: filters,
				})
				if err != nil {
					t.Fatal(err)
				}
				got := []string{}
				for _, field := range prepared.FacetFields {
					got = append(got, field.Code)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("область=%q выбрано=%d цвет=%+v: %v вместо %v", id, selected, color, got, want)
				}
			}
		}
	}
}
