package taxonomydata

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func productTypeDefinition(t *testing.T) taxonomy.Definition {
	t.Helper()
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	entries := dictionaryEntries(t, "product_type")
	d := release.Definition()
	for i := range d.Attributes {
		if d.Attributes[i].Code == "product_type" {
			for _, entry := range entries {
				d.Attributes[i].Values = append(d.Attributes[i].Values, entry.Canonical)
			}
		}
	}
	return d
}

func TestBundledProductType(t *testing.T) {
	d := productTypeDefinition(t)
	field := d.Fields()["product_type"]
	if d.Version != 15 || field.Name != "Тип" || field.Type != "enum" || field.Dictionary != "product_type" ||
		!field.Facet || field.FacetPinned == nil || !*field.FacetPinned ||
		field.Multiple || field.RequiredReason == "" {
		t.Fatalf("тип: %+v", field)
	}
	count := 0
	var walk func(taxonomy.Category, bool)
	walk = func(node taxonomy.Category, required bool) {
		if len(node.Children) > 0 {
			for _, child := range node.Children {
				walk(child, required)
			}
			return
		}
		schema := d.Schemas[node.Schema]
		hasType := slices.Contains(schema.Attributes, "product_type")
		if required && !hasType {
			t.Errorf("нет типа у %s", node.ID)
		}
		if !hasType {
			return
		}
		count++
		if slices.Contains(schema.Optional, "product_type") || slices.Contains(schema.HiddenFacets, "product_type") {
			t.Errorf("тип необязателен или скрыт у %s", node.ID)
		}
		invalid := []json.RawMessage{nil, json.RawMessage(`"неизвестный вид"`), json.RawMessage(`["Лоферы"]`)}
		for _, raw := range invalid {
			attributes := map[string]json.RawMessage{}
			if raw != nil {
				attributes["product_type"] = raw
			}
			publication := d.Check(&taxonomy.Card{
				CategoryID: node.ID,
				Published:  true,
				Attributes: attributes,
			})
			if !slices.ContainsFunc(publication.Issues, func(issue taxonomy.Issue) bool {
				return issue.Attribute == "product_type"
			}) {
				t.Errorf("принят неверный тип %s: %s", node.ID, raw)
			}
		}
		publication := d.Check(&taxonomy.Card{
			CategoryID: node.ID,
			Published:  true,
			Attributes: map[string]json.RawMessage{"product_type": json.RawMessage(`"Лоферы"`)},
		})
		if slices.ContainsFunc(publication.Issues, func(issue taxonomy.Issue) bool {
			return issue.Attribute == "product_type"
		}) {
			t.Errorf("тип продавца подменён категорией %s", node.ID)
		}
	}
	for _, root := range d.Categories {
		walk(root, root.ID == "clothing" || root.ID == "shoes")
	}
	if count != 501 {
		t.Fatalf("листовых категорий с типом: %d", count)
	}
}

func TestBundledProductTypeFacetScope(t *testing.T) {
	store := fake.NewStore()
	store.SeedTaxonomy(productTypeDefinition(t))
	queries := appcatalog.NewQueries(store, fake.NewIndex(), nil, nil, nil, fake.NewClock(time.Time{}))
	for _, id := range []string{"", "clothing", "shoes", "clothing.women", "shoes.men-loafers"} {
		query, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{
			CategoryID: id,
			Attributes: map[string]ports.AttributeFilter{"product_type": {Values: []string{"Лоферы", "Кроссовки"}}},
		})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		found := slices.ContainsFunc(query.FacetFields, func(field taxonomy.Attribute) bool {
			return field.Code == "product_type"
		})
		if !found || len(query.FacetFields) > appcatalog.MaxAttributeFacets ||
			query.Attributes["product_type"].Type != "enum" {
			t.Fatalf("%s: потерян тип или превышен бюджет: %+v", id, query)
		}
	}
}

func TestBundledProductTypePinControlsSearchFacet(t *testing.T) {
	ctx := context.Background()
	d := productTypeDefinition(t)
	store := fake.NewStore()
	index := fake.NewIndex()
	const category = "shoes.men-loafers"
	docs := make([]ports.OfferDocument, 20)
	for i := range docs {
		docs[i] = ports.OfferDocument{
			ID:           fmt.Sprintf("t1~p%d", i),
			CategoryPath: []string{category},
			Listed:       true,
		}
	}
	docs[0].Attributes = map[string]json.RawMessage{"product_type": json.RawMessage(`"Лоферы"`)}
	docs[1].Attributes = map[string]json.RawMessage{"product_type": json.RawMessage(`"Кроссовки"`)}
	if err := index.Put(ctx, docs); err != nil {
		t.Fatal(err)
	}
	queries := appcatalog.NewQueries(store, index, fake.NewPlatform(), nil, nil, fake.NewClock(time.Time{}))
	for _, pinned := range []bool{true, false} {
		if !pinned {
			for i := range d.Attributes {
				if d.Attributes[i].Code == "product_type" {
					d.Attributes[i].FacetPinned = &pinned
					break
				}
			}
		}
		store.SeedTaxonomy(d)
		page, err := queries.Search(ctx, ports.OfferQuery{CategoryID: category})
		if err != nil || page.Total != len(docs) {
			t.Fatalf("выдача: total=%d, %v", page.Total, err)
		}
		found := slices.ContainsFunc(page.Facets, func(facet ports.Facet) bool {
			return facet.Code == "attr.product_type"
		})
		if found != pinned {
			t.Fatalf("тип с охватом 10%%: закреплён=%v, показан=%v", pinned, found)
		}
	}
}

func TestProductTypeNameExceptionIsNarrow(t *testing.T) {
	field := productTypeDefinition(t).Fields()["product_type"]
	if err := auditAttribute(field); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*taxonomy.Attribute){
		func(a *taxonomy.Attribute) { a.Code = "a1" },
		func(a *taxonomy.Attribute) { a.Name = "Вид" },
		func(a *taxonomy.Attribute) { a.Dictionary = "a1" },
		func(a *taxonomy.Attribute) { a.Type = "string" },
	} {
		changed := field
		change(&changed)
		if err := auditAttribute(changed); err == nil {
			t.Fatalf("широкое исключение: %+v", changed)
		}
	}
}
