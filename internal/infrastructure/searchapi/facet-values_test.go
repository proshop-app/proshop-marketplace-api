package searchapi

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestEncodeFacetValuesKeepsOriginalValue(t *testing.T) {
	source := ports.OfferDocument{
		Attributes:           map[string]json.RawMessage{"a1": json.RawMessage(`"v1"`)},
		AttributeFacetValues: map[string][]string{"a1": {"v2", "v3"}},
	}
	doc, err := toDocument(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Attributes) != 1 || doc.Attributes[0].Value != "v1" ||
		!reflect.DeepEqual(doc.Attributes[0].FacetValues, []string{"v2", "v3"}) {
		t.Fatalf("неверная характеристика: %+v", doc.Attributes)
	}
	source.Attributes["a1"] = json.RawMessage(`1`)
	if _, err := toDocument(source); err == nil {
		t.Fatal("число получило значения строкового фасета")
	}
}
