package catalog_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestHiddenFacetsScopeAndFilterUnion(t *testing.T) {
	var schemas map[string]taxonomy.Schema
	body := []byte(`{
		"s1": {"attributes": ["a1", "a2", "color"], "hiddenFacets": ["a1", "color"]},
		"s2": {"attributes": ["a1", "a2", "color"]}
	}`)
	err := json.Unmarshal(body, &schemas)
	if err != nil {
		t.Fatal(err)
	}
	d := taxonomy.Definition{
		Attributes: []taxonomy.Attribute{
			{Code: "a1", Type: "string", Facet: true},
			{Code: "a2", Type: "string", Facet: true},
			{Code: "color", Type: "string", Facet: true},
		},
		Schemas: schemas,
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
		"":   {"a1", "a2", "color"},
		"c1": {"a1", "a2", "color"},
		"c2": {"a2"},
		"c3": {"a2"},
		"c5": {"a1", "a2", "color"},
	} {
		t.Run(id, func(t *testing.T) {
			prepared, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{
				CategoryID: id,
				Attributes: map[string]ports.AttributeFilter{
					"a1":    {Values: []string{"v1"}},
					"color": {Values: []string{"v1"}},
				},
			})
			if err != nil || prepared.Attributes["a1"].Type != "string" ||
				prepared.Attributes["color"].Type != "string" {
				t.Fatalf("скрытый фасет изменил допустимость фильтра: %+v, %v", prepared, err)
			}
			codes := []string{}
			for _, field := range prepared.FacetFields {
				codes = append(codes, field.Code)
			}
			if !reflect.DeepEqual(codes, want) {
				t.Fatalf("фасеты %v, ожидаются %v", codes, want)
			}
		})
	}
}
