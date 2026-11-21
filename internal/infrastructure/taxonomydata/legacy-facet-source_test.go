package taxonomydata

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestLegacyFacetFieldsResolveFromSource(t *testing.T) {
	const data = `{
		"attributes":[{"code":"a1","facet":true,"facetPinned":true},{"code":"a2","facet":true}],
		"schemas":{
			"base":{"abstract":true,"attributes":["a1","a2"],"hiddenFacets":["a1"]},
			"leaf":{"extends":"base"}
		},
		"categories":[{"id":"c1","schema":"leaf"}]
	}`
	var source artifactDefinition
	if err := decodeStrictJSON([]byte(data), &source); err != nil {
		t.Fatal(err)
	}
	for _, version := range []int64{15, 16} {
		d, err := source.definition(version)
		if err != nil {
			t.Fatal(err)
		}
		d.Schemas, err = d.ExpandSchemaSource()
		if err != nil {
			t.Fatal(err)
		}
		d, err = d.CompileFacets(false)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(d.Schemas["leaf"].Filters, []taxonomy.Filter{{Code: "a2", Primary: true}}) {
			t.Fatalf("выпуск %d: %v", version, d.Schemas["leaf"].Filters)
		}
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(`"hiddenFacets"`)) || bytes.Contains(raw, []byte(`"facetPinned"`)) {
			t.Fatal("историческое поле попало в снимок")
		}
	}
	if _, err := source.definition(17); err == nil {
		t.Fatal("исторический формат принят в поставке 17")
	}
}
