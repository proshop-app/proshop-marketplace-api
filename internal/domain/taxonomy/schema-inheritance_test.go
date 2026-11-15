package taxonomy_test

import (
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestExpandSchemas(t *testing.T) {
	source := map[string]taxonomy.Schema{
		"base": {
			Abstract:   true,
			Attributes: []string{"a1"},
			Optional:   []string{"a1"},
		},
		"item": {
			Extends:    "base",
			Attributes: []string{"a2"},
		},
	}
	got, err := taxonomy.ExpandSchemas(source)
	want := map[string]taxonomy.Schema{
		"item": {
			Attributes: []string{"a1", "a2"},
			Optional:   []string{"a1"},
		},
	}
	if err != nil || !reflect.DeepEqual(got, want) || len(source["base"].Attributes) != 1 {
		t.Fatal(got, err)
	}
	for _, invalid := range []map[string]taxonomy.Schema{
		{"a": {Extends: "missing"}},
		{"a": {Extends: "b"}, "b": {Extends: "a"}},
		{"a": {Attributes: []string{"a1", "a1"}}},
		{"a": {Attributes: []string{"a1"}, Optional: []string{"a2"}}},
		{"a": {Attributes: []string{"a1"}, Optional: []string{"a1", "a1"}}},
		{"a": {}},
	} {
		if _, err := taxonomy.ExpandSchemas(invalid); err == nil {
			t.Fatal("принято неверное наследование", invalid)
		}
	}
}

func TestAbstractSchemaCannotHideUnknownAttribute(t *testing.T) {
	definition := taxonomy.Definition{
		Attributes: []taxonomy.Attribute{{Code: "a1", Type: "string"}},
		Schemas: map[string]taxonomy.Schema{
			"unused": {Abstract: true, Attributes: []string{"missing"}},
			"active": {Attributes: []string{"a1"}},
		},
	}
	if _, err := definition.ExpandSchemaSource(); err == nil {
		t.Fatal("неизвестная ссылка в абстрактной схеме принята")
	}
}
