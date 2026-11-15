package taxonomy

import (
	"encoding/json"
	"strings"
	"testing"
)

func identityRelease() Release {
	return Release{
		Version:    1,
		Checksum:   strings.Repeat("a", 64),
		Categories: []Category{{ID: "c1", Name: "Категория", Slug: "category", Schema: "s1"}},
		Attributes: []Attribute{
			{Code: "model", Name: "Модель", Type: "string"},
			{Code: "axis", Name: "Ось", Type: "number"},
			{Code: "exact", Name: "Код", Type: "string"},
		},
		Schemas: map[string]Schema{
			"s1": {
				Attributes: []string{"model", "axis", "exact"},
				Optional:   []string{"exact"},
				Identity:   &SchemaIdentity{Model: []string{"model"}, Axes: []string{"axis"}, Exact: []string{"exact"}},
			},
		},
	}
}

func TestReleaseIdentityRules(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Schema)
		valid  bool
	}{
		{name: "valid", change: func(*Schema) {}, valid: true},
		{name: "empty model", change: func(s *Schema) { s.Identity.Model = nil }},
		{name: "unknown model", change: func(s *Schema) { s.Identity.Model = []string{"missing"} }},
		{name: "unknown axes", change: func(s *Schema) { s.Identity.Axes = []string{"missing"} }},
		{name: "unknown exact", change: func(s *Schema) { s.Identity.Exact = []string{"missing"} }},
		{name: "duplicate model", change: func(s *Schema) { s.Identity.Model = []string{"model", "model"} }},
		{name: "duplicate axes", change: func(s *Schema) { s.Identity.Axes = []string{"axis", "axis"} }},
		{name: "duplicate exact", change: func(s *Schema) { s.Identity.Exact = []string{"exact", "exact"} }},
		{name: "model axes overlap", change: func(s *Schema) { s.Identity.Axes = []string{"model"} }},
		{name: "model exact overlap", change: func(s *Schema) { s.Identity.Exact = []string{"model"} }},
		{name: "axes exact overlap", change: func(s *Schema) { s.Identity.Exact = []string{"axis"} }},
		{name: "optional model", change: func(s *Schema) { s.Optional = []string{"model"} }},
		{name: "optional axis", change: func(s *Schema) { s.Optional = []string{"axis"} }},
		{
			name: "warning release",
			change: func(s *Schema) {
				s.Optional = []string{"model", "axis"}
				warnings := true
				s.IdentityWarnings = &warnings
			},
			valid: true,
		},
		{
			name: "no axes or exact",
			change: func(s *Schema) {
				s.Identity.Axes = nil
				s.Identity.Exact = nil
			},
			valid: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			release := identityRelease()
			schema := release.Schemas["s1"]
			test.change(&schema)
			release.Schemas["s1"] = schema
			err := ValidateRelease(release)
			if (err == nil) != test.valid {
				t.Fatal(err)
			}
			if err != nil && !strings.Contains(err.Error(), "s1") {
				t.Fatal("нет кода схемы", err)
			}
		})
	}
}

func TestIdentityAxisTypes(t *testing.T) {
	for _, kind := range []string{"boolean", "array"} {
		release := identityRelease()
		release.Attributes[1].Type = kind
		err := ValidateRelease(release)
		if err == nil || !strings.Contains(err.Error(), "s1") || !strings.Contains(err.Error(), "axis") {
			t.Fatal("нет диагностики типа оси", kind, err)
		}
	}
}

func TestIdentityIncompleteIsWarning(t *testing.T) {
	release := identityRelease()
	schema := release.Schemas["s1"]
	schema.Optional = []string{"model", "axis", "exact"}
	warnings := true
	schema.IdentityWarnings = &warnings
	release.Schemas["s1"] = schema
	definition := release.Definition()
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`" "`)} {
		publication := definition.Check(&Card{
			CategoryID: "c1",
			Published:  true,
			Attributes: map[string]json.RawMessage{"model": raw, "axis": json.RawMessage(`0`)},
		})
		if !publication.Published || len(publication.Issues) != 1 ||
			publication.Issues[0] != (Issue{Code: "identity_incomplete", Attribute: "model"}) {
			t.Fatalf("%+v", publication)
		}
	}
	category, _, ok := definition.Category("c1")
	if !ok || category.Identity == nil || category.Identity.Model[0] != "model" {
		t.Fatal(category)
	}
}
