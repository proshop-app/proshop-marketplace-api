package postgres

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestDecodeStoredTaxonomyLegacyFacetFields(t *testing.T) {
	const lastPublished = `{
		"version":5,
		"attributes":[{"code":"a1","facet":true,"facetPinned":true},{"code":"a2","facet":true}],
		"schemas":{"s1":{"attributes":["a1","a2"],"hiddenFacets":["a1"]}},
		"categories":[{"id":"c1","schema":"s1"}]
	}`
	snapshot, err := decodeTaxonomySnapshot([]byte(lastPublished))
	if err != nil {
		t.Fatal(err)
	}
	want := []taxonomy.Filter{{Code: "a2", Primary: true}}
	if snapshot.Version != 5 || !slices.Equal(snapshot.FacetKeys, []string{"a2"}) ||
		!slices.Equal(snapshot.Schemas["s1"].Filters, want) || !slices.Equal(snapshot.RootFilters, want) {
		t.Fatalf("исторический снимок: %+v", snapshot)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := decodeTaxonomySnapshot(raw)
	if err != nil || !reflect.DeepEqual(stored, snapshot) {
		t.Fatalf("повторное чтение изменило снимок: %+v, %v", stored, err)
	}
}
