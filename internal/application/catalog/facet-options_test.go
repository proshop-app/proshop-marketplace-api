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
	if got := orderFacet(facet, []taxonomy.Attribute{field}); !reflect.DeepEqual(got.Values, want) {
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
	got := facetValuesMeta(facet, fields)
	if !reflect.DeepEqual(got.Values, want) {
		t.Fatalf("неверные образцы: %+v", got.Values)
	}
	if facet.Values[0].Meta != nil {
		t.Fatal("образцы записаны в исходный фасет")
	}
	size := ports.Facet{Code: "attr.size", Values: []ports.FacetBucket{{Value: "M", Count: 1}}}
	if got := facetValuesMeta(size, fields); got.Values[0].Meta != nil {
		t.Fatal("образец у значения без valuesMeta")
	}
}
