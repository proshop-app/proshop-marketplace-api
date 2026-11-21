package catalog

import (
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestFacetOptionsOrderAndAllowedValues(t *testing.T) {
	field := taxonomy.Attribute{
		Code:         "a1",
		Type:         "enum",
		Values:       []string{"v1", "f1"},
		FacetOptions: []string{"f1", "f2", "f3"},
	}
	facet := ports.Facet{
		Code: "attr.a1",
		Values: []ports.FacetBucket{
			{Value: "f3", Count: 0},
			{Value: "foreign", Count: 3},
			{Value: "f2", Count: 2},
			{Value: "f1", Count: 0},
		},
	}
	want := []ports.FacetBucket{
		{Value: "f1", Count: 0},
		{Value: "f2", Count: 2},
		{Value: "f3", Count: 0},
	}
	if got := orderFacet(facet, field); !reflect.DeepEqual(got.Values, want) {
		t.Fatal(got)
	}
	for _, value := range field.FacetOptions {
		if err := validateAttributeFilters(
			map[string]ports.AttributeFilter{"a1": {Values: []string{value}}},
			map[string]taxonomy.Attribute{"a1": field},
		); err != nil {
			t.Fatal(value, err)
		}
	}
	if err := validateAttributeFilters(
		map[string]ports.AttributeFilter{"a1": {Values: []string{"v1"}}},
		map[string]taxonomy.Attribute{"a1": field},
	); err == nil {
		t.Fatal("значение характеристики принято вместо значения фасета")
	}
}

func TestLegacyColorFacetKeepsPaletteOrderAndSelection(t *testing.T) {
	query := ports.OfferQuery{
		FacetFields: []taxonomy.Attribute{
			{Code: "color", Type: "enum", Facet: true, FacetOptions: []string{"Белый", "Красный"}},
		},
	}
	white := ports.FacetBucket{Value: "Белый", Count: 1}
	red := ports.FacetBucket{Value: "Красный", Count: 2}
	facets := []ports.Facet{{Code: "attr.color", Count: 3, Values: []ports.FacetBucket{red, white}}}
	display := facetDisplay{
		taxonomy: &taxonomyDisplay{fields: map[string]taxonomy.Attribute{"color": query.FacetFields[0]}},
		order:    []taxonomy.Filter{{Code: "color", Primary: true}},
	}
	got := displayFacets(facets, query, display)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Values, []ports.FacetBucket{white, red}) {
		t.Fatalf("цвет с малым охватом потерян или изменён порядок палитры: %+v", got)
	}

	facets[0].Count = 1
	facets[0].Values = []ports.FacetBucket{white}
	query.Attributes = map[string]ports.AttributeFilter{"color": {Values: []string{white.Value}}}
	got = displayFacets(facets, query, display)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Values, []ports.FacetBucket{white}) {
		t.Fatalf("единственный выбранный цвет недоступен для снятия: %+v", got)
	}
}

func TestFacetValuesMetaFromSnapshot(t *testing.T) {
	red := taxonomy.ValueMeta{Hex: "#FF0000"}
	many := taxonomy.ValueMeta{Swatch: "multicolor"}
	fields := []taxonomy.Attribute{
		{Code: "size", Type: "enum"},
		{Code: "color", Type: "enum", ValuesMeta: map[string]taxonomy.ValueMeta{"Красный": red, "Разноцветный": many}},
	}
	facet := ports.Facet{
		Code: "attr.color",
		Values: []ports.FacetBucket{
			{Value: "Красный", Count: 2},
			{Value: "Разноцветный", Count: 1},
			{Value: "Без образца", Count: 1},
		},
	}
	want := []ports.FacetBucket{
		{Value: "Красный", Count: 2, Meta: &red},
		{Value: "Разноцветный", Count: 1, Meta: &many},
		{Value: "Без образца", Count: 1},
	}
	got := displayFacetValues(facet, fields[1], facetDisplay{})
	if !reflect.DeepEqual(got.Values, want) {
		t.Fatalf("неверные образцы: %+v", got.Values)
	}
	if facet.Values[0].Meta != nil {
		t.Fatal("образцы записаны в исходный фасет")
	}
	size := ports.Facet{Code: "attr.size", Values: []ports.FacetBucket{{Value: "M", Count: 1}}}
	if got := displayFacetValues(size, fields[0], facetDisplay{}); got.Values[0].Meta != nil {
		t.Fatal("образец у значения без valuesMeta")
	}
}
