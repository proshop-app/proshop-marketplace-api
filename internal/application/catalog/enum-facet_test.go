package catalog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestEnumFacetAndFilter(t *testing.T) {
	ctx := context.Background()
	d := taxonomy.Definition{
		Attributes: []taxonomy.Attribute{
			{
				Code:        "a1",
				Name:        "Вид",
				Type:        "enum",
				Dictionary:  "d1",
				Values:      []string{"v1", "v2"},
				Facet:       true,
				FacetPinned: true,
			},
		},
		Schemas: map[string]taxonomy.Schema{
			"s1": {Attributes: []string{"a1"}},
			"s2": {},
		},
		Categories: []taxonomy.Category{
			{
				ID: "c1",
				Children: []taxonomy.Category{
					{ID: "c2", Name: "Категория 2", Schema: "s1"},
					{ID: "c3", Name: "Категория 3", Schema: "s1"},
				},
			},
			{ID: "c4", Schema: "s2"},
		},
	}
	store := fake.NewStore()
	store.SeedTaxonomy(d)
	index := fake.NewIndex()
	for i := 0; i < 30; i++ {
		id := "c4"
		if i < 3 {
			id = "c3"
		}
		if i == 0 {
			id = "c2"
		}
		attributes := map[string]json.RawMessage{}
		if id == "c2" {
			attributes["a1"] = json.RawMessage(`"v1"`)
		}
		if id == "c3" {
			attributes["a1"] = json.RawMessage(`"v2"`)
		}
		publication := d.Check(&taxonomy.Card{CategoryID: id, Published: true, Attributes: attributes})
		doc := ports.OfferDocument{
			ID:           fmt.Sprintf("t1~p%d", i),
			TenantID:     "t1",
			Name:         "Товар",
			CategoryID:   id,
			CategoryPath: publication.CategoryPath,
			Attributes:   publication.Attributes,
			Listed:       true,
			Visible:      true,
			Version:      1,
		}
		if err := index.Put(ctx, []ports.OfferDocument{doc}); err != nil {
			t.Fatal(err)
		}
	}
	queries := appcatalog.NewQueries(store, index, fake.NewPlatform(), nil, nil, fake.NewClock(time.Time{}))
	for _, category := range []string{"", "c1"} {
		query := ports.OfferQuery{CategoryID: category, Text: "Товар"}
		page, err := queries.Search(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		facet := findFacet(t, page.Facets, "attr.a1")
		if len(facet.Values) != 2 || facet.Values[0].Value != "v2" || facet.Values[0].Count != 2 {
			t.Fatalf("порядок и охват: %+v", facet)
		}
		query.Attributes = map[string]ports.AttributeFilter{"a1": {Values: []string{"v1"}}}
		page, err = queries.Search(ctx, query)
		if err != nil || page.Total != 1 {
			t.Fatalf("выбран один тип: %+v %v", page, err)
		}
		facet = findFacet(t, page.Facets, "attr.a1")
		if len(facet.Values) != 2 || facet.Values[0].Value != "v2" || facet.Values[1].Value != "v1" {
			t.Fatalf("выбор типа скрыл альтернативы: %+v", facet)
		}
		query.Attributes = map[string]ports.AttributeFilter{"a1": {Values: []string{"v1", "v2"}}}
		page, err = queries.Search(ctx, query)
		if err != nil || page.Total != 3 {
			t.Fatalf("отбор нескольких значений: %+v %v", page, err)
		}
		facet = findFacet(t, page.Facets, "attr.a1")
		if len(facet.Values) != 2 {
			t.Fatalf("собственный отбор скрыл альтернативы: %+v", facet)
		}
	}
	page, err := queries.Search(ctx, ports.OfferQuery{CategoryID: "c2"})
	if err != nil {
		t.Fatal(err)
	}
	for _, facet := range page.Facets {
		if facet.Code == "attr.a1" {
			t.Fatal("одно значение не должно показываться")
		}
	}
	page, err = queries.Search(ctx, ports.OfferQuery{
		CategoryID: "c2",
		Attributes: map[string]ports.AttributeFilter{"a1": {Values: []string{"v1"}}},
	})
	if err != nil || page.Total != 1 {
		t.Fatalf("выбран единственный тип: %+v %v", page, err)
	}
	facet := findFacet(t, page.Facets, "attr.a1")
	if len(facet.Values) != 1 || facet.Values[0].Value != "v1" {
		t.Fatalf("выбранный единственный тип недоступен для снятия: %+v", facet)
	}
}
