package taxonomy_test

import (
	"fmt"
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

func TestIdentityInheritanceAndOverride(t *testing.T) {
	warnings := true
	strict := false
	definition := taxonomy.Definition{
		Attributes: []taxonomy.Attribute{
			{Code: "a1", Type: "string"},
			{Code: "a2", Type: "number"},
			{Code: "a3", Type: "string"},
		},
		Schemas: map[string]taxonomy.Schema{
			"base": {
				Abstract:         true,
				Attributes:       []string{"a1", "a2"},
				Identity:         &taxonomy.SchemaIdentity{Model: []string{"a1"}, Axes: []string{"a2"}},
				IdentityWarnings: &warnings,
			},
			"middle": {
				Abstract: true,
				Extends:  "base",
			},
			"inherited": {
				Extends:  "middle",
				Optional: []string{"a2"},
			},
			"replaced": {
				Extends:          "middle",
				Attributes:       []string{"a3"},
				Identity:         &taxonomy.SchemaIdentity{Model: []string{"a3"}, Axes: []string{}},
				IdentityWarnings: &strict,
			},
		},
	}
	got, err := definition.ExpandSchemaSource()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !reflect.DeepEqual(got["inherited"].Identity, definition.Schemas["base"].Identity) ||
		got["inherited"].IdentityWarnings == nil || !*got["inherited"].IdentityWarnings {
		t.Fatal("потеряно наследование через абстрактного родителя", got)
	}
	replaced := got["replaced"]
	if !reflect.DeepEqual(replaced.Identity, definition.Schemas["replaced"].Identity) ||
		replaced.IdentityWarnings == nil || *replaced.IdentityWarnings {
		t.Fatal("переопределение должно заменять блок целиком и сохранять false", replaced)
	}
	got["inherited"].Identity.Model[0] = "changed"
	*got["inherited"].IdentityWarnings = false
	if definition.Schemas["base"].Identity.Model[0] != "a1" || !warnings {
		t.Fatal("разворачивание разделяет изменяемые данные с исходником")
	}
}

func TestInheritedIdentityValidatesEffectiveSchema(t *testing.T) {
	for _, abstract := range []bool{false, true} {
		for _, invalid := range []string{"optional", "unknown", "boolean"} {
			t.Run(fmt.Sprintf("%t/%s", abstract, invalid), func(t *testing.T) {
				definition := taxonomy.Definition{
					Attributes: []taxonomy.Attribute{
						{Code: "a1", Type: "string"},
						{Code: "a2", Type: "number"},
					},
					Schemas: map[string]taxonomy.Schema{
						"base": {
							Abstract:   true,
							Attributes: []string{"a1", "a2"},
							Identity:   &taxonomy.SchemaIdentity{Model: []string{"a1"}, Axes: []string{"a2"}},
						},
						"child": {
							Abstract: abstract,
							Extends:  "base",
						},
					},
				}
				child := definition.Schemas["child"]
				switch invalid {
				case "optional":
					child.Optional = []string{"a2"}
				case "unknown":
					child.Identity = &taxonomy.SchemaIdentity{Model: []string{"missing"}}
				case "boolean":
					definition.Attributes[1].Type = "boolean"
				}
				definition.Schemas["child"] = child
				if _, err := definition.ExpandSchemaSource(); err == nil {
					t.Fatal("принята неверная итоговая идентичность", invalid)
				}
			})
		}
	}
}

func TestIdentityWarningsFalseOverridesInheritedTrue(t *testing.T) {
	warnings := true
	strict := false
	definition := taxonomy.Definition{
		Attributes: []taxonomy.Attribute{{Code: "a1", Type: "string"}},
		Schemas: map[string]taxonomy.Schema{
			"base": {
				Abstract:         true,
				Attributes:       []string{"a1"},
				Optional:         []string{"a1"},
				Identity:         &taxonomy.SchemaIdentity{Model: []string{"a1"}},
				IdentityWarnings: &warnings,
			},
			"child": {
				Extends:          "base",
				IdentityWarnings: &strict,
			},
		},
	}
	if _, err := definition.ExpandSchemaSource(); err == nil {
		t.Fatal("явный false должен включить проверку унаследованной обязательности")
	}
}
