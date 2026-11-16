package catalog

import (
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestFacetOptionsOrderAndComputedBucket(t *testing.T) {
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
	want := []ports.FacetBucket{{Value: "f1", Count: 0}, {Value: "f2", Count: 2}}
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
