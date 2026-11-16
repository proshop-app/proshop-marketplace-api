package taxonomy_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestHiddenFacetsInheritance(t *testing.T) {
	var source map[string]taxonomy.Schema
	body := []byte(`{
		"base": {"abstract": true, "attributes": ["a1"], "hiddenFacets": ["a1"]},
		"middle": {"abstract": true, "extends": "base"},
		"child": {"extends": "middle", "attributes": ["a2"], "hiddenFacets": ["a1", "a2"]},
		"sibling": {"extends": "middle"}
	}`)
	err := json.Unmarshal(body, &source)
	if err != nil {
		t.Fatal(err)
	}
	got, err := taxonomy.ExpandSchemas(source)
	if err != nil {
		t.Fatal(err)
	}
	body, err = json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]struct {
		HiddenFacets []string `json:"hiddenFacets"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !reflect.DeepEqual(wire["child"].HiddenFacets, []string{"a1", "a2"}) ||
		!reflect.DeepEqual(wire["sibling"].HiddenFacets, []string{"a1"}) {
		t.Fatalf("потеряно наследование или дополнение hiddenFacets: %s", body)
	}
	got["child"].HiddenFacets[0] = "changed"
	if source["base"].HiddenFacets[0] != "a1" || got["sibling"].HiddenFacets[0] != "a1" {
		t.Fatal("список hiddenFacets разделяется с исходником или соседней схемой")
	}
}

func TestHiddenFacetsRejectInvalidCodes(t *testing.T) {
	for _, hidden := range []string{`["missing"]`, `["a1","a1"]`} {
		t.Run(hidden, func(t *testing.T) {
			var schema taxonomy.Schema
			body := `{"attributes":["a1"],"hiddenFacets":` + hidden + `}`
			if err := json.Unmarshal([]byte(body), &schema); err != nil {
				t.Fatal(err)
			}
			if _, err := taxonomy.ExpandSchemas(map[string]taxonomy.Schema{"s1": schema}); err == nil {
				t.Error("принят неверный hiddenFacets при разворачивании")
			}
			d := definition()
			d.Schemas["s1"] = schema
			if err := d.Validate(); err == nil {
				t.Fatal("принят неверный hiddenFacets в сохранённом снимке")
			}
		})
	}
}

func TestHiddenFacetsPreserveCardAndOmitEmptyJSON(t *testing.T) {
	d := definition()
	schema := d.Schemas["s1"]
	body, _ := json.Marshal(schema)
	var wire map[string]json.RawMessage
	_ = json.Unmarshal(body, &wire)
	if _, exists := wire["hiddenFacets"]; exists {
		t.Fatal("пустое поле должно отсутствовать в JSON")
	}
	wire["hiddenFacets"] = json.RawMessage(`["a1"]`)
	body, _ = json.Marshal(wire)
	_ = json.Unmarshal(body, &schema)
	d.Schemas["s1"] = schema
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.Check(card()), definition().Check(card())) {
		t.Fatal("скрытие фасета изменило проверку карточки")
	}
}
