package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	pricecatalog "github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	pricemoney "github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestCanonicalFiltersAndCoverage(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	index := fake.NewIndex()
	store.SeedTaxonomy(taxonomy.Definition{Version: 1, Categories: []taxonomy.Category{{
		ID:   "c1",
		Slug: "c1",
		Name: "Категория",
		Attributes: []taxonomy.Attribute{
			{Code: "a1", Name: "Атрибут 1", Type: "enum", Values: []string{"v1", "v2"}, Facet: true},
			{Code: "a2", Name: "Атрибут 2", Type: "number", Facet: true},
			{Code: "a3", Name: "Атрибут 3", Type: "boolean", Facet: true},
			{Code: "a4", Name: "Атрибут 4", Type: "string"},
		},
	}}})
	for i := 0; i < 10; i++ {
		attrs := map[string]json.RawMessage{"a2": json.RawMessage(fmt.Sprint(i)), "a4": json.RawMessage(`"Текст"`)}
		if i < 2 {
			attrs["a1"] = json.RawMessage(`"v1"`)
		}
		if i == 0 {
			attrs["a3"] = json.RawMessage(`false`)
		}
		doc := ports.OfferDocument{
			ID:           fmt.Sprintf("t1~p%d", i),
			TenantID:     "t1",
			CategoryID:   "c1",
			CategoryPath: []string{"c1"},
			Attributes:   attrs,
			Listed:       true,
			Visible:      true,
			Version:      1,
			PriceProfiles: []pricecatalog.PriceProfile{
				{
					Key:            pricecatalog.DefaultPriceProfile,
					Price:          pricemoney.FromKopecks(0),
					EffectivePrice: pricemoney.FromKopecks(0),
				},
			},
		}
		if err := index.Put(ctx, []ports.OfferDocument{doc}); err != nil {
			t.Fatal(err)
		}
	}
	q := appcatalog.NewQueries(store, index, fake.NewPlatform())
	page, err := q.Search(ctx, ports.OfferQuery{Limit: 1, Offset: 8})
	if err != nil || page.Total != 10 || len(page.Facets) != 6 {
		t.Fatalf("%+v %v", page, err)
	}
	found := map[string]ports.Facet{}
	for _, f := range page.Facets {
		found[f.Code] = f
	}
	if found["attr.a1"].Count != 2 || found["attr.a2"].Min.String() != "0" || found["attr.a2"].Max.String() != "9" {
		t.Fatalf("фасеты считаются до пагинации: %+v", found)
	}
	if _, ok := found["attr.a3"]; ok {
		t.Fatal("редкое поле показывается")
	}
	if _, ok := found["attr.a4"]; ok {
		t.Fatal("поле без facet показывается")
	}
	page, err = q.Search(ctx, ports.OfferQuery{Attributes: map[string]ports.AttributeFilter{
		"a1": {Values: []string{"v1", "v2"}},
		"a2": {Min: "1", Max: "1.5"},
	}})
	if err != nil || page.Total != 1 {
		t.Fatalf("AND, OR, диапазон: %+v %v", page, err)
	}
	page, err = q.Search(ctx, ports.OfferQuery{Attributes: map[string]ports.AttributeFilter{
		"a3": {Values: []string{"false"}},
	}})
	if err != nil || page.Total != 1 {
		t.Fatalf("boolean false: %+v %v", page, err)
	}
	page, err = q.Search(ctx, ports.OfferQuery{Text: "нет совпадений"})
	if err != nil || page.Total != 0 || len(page.Facets) != 4 {
		t.Fatalf("пустая выдача: %+v %v", page, err)
	}
	invalid := []map[string]ports.AttributeFilter{
		{"missing": {Values: []string{"v1"}}},
		{"a2": {Min: "NaN"}},
		{"a2": {Min: "3", Max: "2"}},
		{"a1": {Min: "1"}},
		{"a2": {Min: "1", Values: []string{"2"}}},
		{"a3": {Values: []string{"0"}}},
		{"a1": {Values: []string{"v3"}}},
	}
	for _, attrs := range invalid {
		if _, err := q.Search(ctx, ports.OfferQuery{Attributes: attrs}); !errors.Is(err, appcatalog.ErrInvalidQuery) {
			t.Fatal(err)
		}
	}
}
