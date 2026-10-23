package opensearch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestCanonicalMappingAndQueries(t *testing.T) {
	encoded, err := json.Marshal(indexBody(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`"dynamic":true`,
		`"path_match":"attributes.*"`,
		`"type":"double"`,
		`"taxonomySchema":1`,
	} {
		if !strings.Contains(string(encoded), fragment) {
			t.Fatal(string(encoded))
		}
	}
	body := searchBody(ports.OfferQuery{
		InStockSet: true,
		Attributes: map[string]ports.AttributeFilter{
			"a1": {Values: []string{"v1", "v2"}},
			"a2": {Min: "1.5", Max: "3"},
		},
		FacetFields: []taxonomy.Attribute{{Code: "a2", Type: "number"}},
	})
	encoded, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`"terms":{"attributes.a1":["v1","v2"]}`,
		`"range":{"attributes.a2":{"gte":"1.5","lte":"3"}}`,
		`"term":{"inStock":false}`,
		`"stats":{"field":"attributes.a2"}`,
		`"_source":["price"]`,
	} {
		if !strings.Contains(string(encoded), fragment) {
			t.Fatalf("нет %s: %s", fragment, encoded)
		}
	}
}

func TestReadCanonicalFacetsAndExactPrice(t *testing.T) {
	raw := map[string]json.RawMessage{
		"price": json.RawMessage(`{"doc_count":2,"minPrice":{"hits":{"hits":[{"_source":{"price":9007199254740993}}]}},
		"maxPrice":{"hits":{"hits":[{"_source":{"price":9007199254740995}}]}}}`),
		"attr.a1": json.RawMessage(`{"doc_count":1,"values":{"buckets":[{"key":"false","doc_count":1}]}}`),
		"attr.a2": json.RawMessage(`{"doc_count":2,"values":{"min":1.0,"max":1.75,"count":2}}`),
	}
	facets, err := readFacets(raw, []taxonomy.Attribute{{Code: "a1", Type: "boolean"}, {Code: "a2", Type: "number"}})
	if err != nil || len(facets) != 6 {
		t.Fatalf("%+v %v", facets, err)
	}
	if facets[2].Min.String() != "9007199254740993" || facets[2].Max.String() != "9007199254740995" {
		t.Fatalf("границы цены потеряли точность: %+v", facets[2])
	}
	if facets[4].Values[0].Value != "false" || facets[5].Max.String() != "1.75" {
		t.Fatalf("%+v", facets)
	}
	raw["price"] = json.RawMessage(`{"doc_count":"invalid"}`)
	if _, err := readFacets(raw, nil); err == nil {
		t.Fatal("повреждённый ответ принят")
	}
}
