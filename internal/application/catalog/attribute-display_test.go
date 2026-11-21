package catalog

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestSelectedValuePresentation(t *testing.T) {
	first, second := 0, 1
	field := taxonomy.Attribute{
		Code:                "size",
		Type:                "string",
		Name:                "Точное имя",
		DisplayName:         "Короткое имя",
		DictionarySelection: true,
	}
	d := taxonomy.Definition{
		Version:    1,
		Attributes: []taxonomy.Attribute{field},
		SelectionValues: map[string]taxonomy.DictionaryValues{
			"d1": {
				ValuesMeta: map[string]taxonomy.ValueMeta{
					"44":    {Order: &second},
					"46":    {Order: &first, SecondaryLabel: "XL"},
					"44–46": {Covers: []string{"44", "46"}},
					"8—10":  {},
					"12":    {},
				},
			},
			"d2": {
				ValuesMeta: map[string]taxonomy.ValueMeta{
					"46": {SecondaryLabel: "L"},
				},
			},
		},
	}
	view := &taxonomyDisplay{
		definition: d,
		fields:     d.Fields(),
		sources:    map[string]map[string]string{"c1": {"size": "d1"}, "c2": {"size": "d2"}},
	}
	doc := variantOffer("seller~p1", "46", 1)
	doc.CategoryID = "c1"
	doc.Attributes = map[string]json.RawMessage{"size": json.RawMessage(`"46"`)}
	card := view.attributes(doc)
	if got := card["size"]; got.Name != field.DisplayName || got.Meta == nil || got.Meta.SecondaryLabel != "XL" {
		t.Fatalf("карточка: %+v", got)
	}

	option := VariantOption{Code: "size", Values: []string{"12", "44–46", "44", "8—10", "46"}}
	view.presentVariantOption(&option, []ports.OfferDocument{doc})
	want := []string{"46", "44", "8—10", "12", "44–46"}
	if option.Name != field.DisplayName || !slices.Equal(option.Values, want) {
		t.Fatalf("ось отсортирована по строке: %+v", option)
	}
	if option.ValueDetails[0].Meta.SecondaryLabel != "XL" ||
		!slices.Equal(option.ValueDetails[4].Meta.Covers, []string{"44", "46"}) {
		t.Fatalf("метаданные оси: %+v", option.ValueDetails)
	}

	facet := ports.Facet{
		Code: "attr.size",
		Values: []ports.FacetBucket{
			{Value: "44", Count: 10},
			{Value: "46", Count: 1},
		},
	}
	display := facetDisplay{taxonomy: view, sources: map[string][]string{"size": {"d1"}}}
	got := displayFacetValues(facet, field, display)
	if got.Name != field.DisplayName || got.Values[0].Value != "46" || got.Values[0].Meta.SecondaryLabel != "XL" {
		t.Fatalf("фасет: %+v", got)
	}
	display.sources["size"] = []string{"d1", "d2"}
	mixed := displayFacetValues(facet, field, display)
	if mixed.Values[1].Value != "46" || mixed.Values[1].Meta != nil {
		t.Fatalf("фасет с неоднозначной маркировкой: %+v", mixed)
	}
}
