package taxonomy

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"
)

func TestInheritedFacetOverridesAndLayout(t *testing.T) {
	d := Definition{
		Attributes: []Attribute{
			{Code: "a1", Facet: true},
			{Code: "a2"},
			{Code: "a3", Facet: true},
		},
		Categories: []Category{{ID: "c1", Schema: "leaf"}, {ID: "c2", Schema: "sibling"}},
	}
	body := []byte(`{
		"base":{"abstract":true,"attributes":["a1","a2","a3"],"facetOverrides":{"a1":"off","a2":"on"}},
		"middle":{"abstract":true,"extends":"base","facetLayout":{"primary":["a2"],"additional":["a3"]}},
		"sibling":{"extends":"middle"},
		"leaf":{"extends":"middle","facetOverrides":{"a1":"default","a3":"off","a2":"default"},
			"facetLayout":{"primary":[],"additional":["a1"]}}
	}`)
	if err := json.Unmarshal(body, &d.Schemas); err != nil {
		t.Fatal(err)
	}
	var err error
	d.Schemas, err = d.ExpandSchemaSource()
	if err != nil {
		t.Fatal(err)
	}
	d, err = d.CompileFacets(true)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]Filter{
		"leaf":    {{Code: "a1"}},
		"sibling": {{Code: "a2", Primary: true}, {Code: "a3"}},
	}
	for name, filters := range want {
		if !slices.Equal(d.Schemas[name].Filters, filters) {
			t.Fatalf("%s: %v", name, d.Schemas[name].Filters)
		}
	}
	if !slices.Equal(d.FacetKeys, []string{"a1", "a2", "a3"}) || len(d.RootFilters) != 0 {
		t.Fatalf("объединение ключей %v, пересечение %v", d.FacetKeys, d.RootFilters)
	}
}

func TestFacetLayoutRejectsInvalidPolicies(t *testing.T) {
	d := Definition{
		Attributes: []Attribute{
			{Code: "a1", Facet: true},
			{Code: "a2", Facet: true, Deprecated: true},
		},
		Categories: []Category{{
			ID: "branch",
			Children: []Category{
				{ID: "leaf", Schema: "s1"},
				{ID: "other", Schema: "s2"},
			},
		}},
		Schemas: map[string]Schema{
			"s1": {
				Attributes:  []string{"a1", "a2"},
				FacetLayout: &FacetLayout{Primary: []string{"a1"}, Additional: []string{}},
			},
			"s2": {
				Attributes:  []string{"a2"},
				FacetLayout: &FacetLayout{Primary: []string{}, Additional: []string{}},
			},
		},
	}
	for name, overrides := range map[string]map[string]FacetOverride{
		"unknownAttribute": {"a3": FacetOn},
		"unknownState":     {"a1": "yes"},
		"deprecatedOn":     {"a2": FacetOn},
	} {
		t.Run(name, func(t *testing.T) {
			changed := d
			changed.Schemas = maps.Clone(d.Schemas)
			schema := changed.Schemas["s1"]
			schema.FacetOverrides = overrides
			changed.Schemas["s1"] = schema
			if _, err := changed.CompileFacets(true); err == nil {
				t.Fatal("принято недопустимое переопределение")
			}
		})
	}
	d.Categories[0].FacetLayout = &FacetLayout{
		Primary:    []string{FilterPrice, FilterInStock, FilterSeller, FilterCategory, "a1"},
		Additional: []string{},
	}
	if _, err := d.CompileFacets(true); err == nil {
		t.Fatal("ветка приняла фильтр, недоступный одному из листьев")
	}
	d.Categories[0].FacetLayout = nil
	schema := d.Schemas["s1"]
	schema.FacetLayout.Primary = []string{"@price"}
	d.Schemas["s1"] = schema
	if _, err := d.CompileFacets(true); err == nil {
		t.Fatal("лист принял встроенный фильтр в схеме")
	}
}

func TestFacetPolicyDoesNotChangePublication(t *testing.T) {
	before := Definition{
		Version:    1,
		Attributes: []Attribute{{Code: "a1", Type: "number", Facet: true}},
		Schemas:    map[string]Schema{"s1": {Attributes: []string{"a1"}}},
		Categories: []Category{{ID: "c1", Schema: "s1"}},
	}
	after := before
	after.Attributes = slices.Clone(before.Attributes)
	after.Attributes[0].Facet = false
	after.Schemas = map[string]Schema{"s1": {
		Attributes:     []string{"a1"},
		FacetOverrides: map[string]FacetOverride{"a1": FacetOff},
		FacetLayout:    &FacetLayout{Primary: []string{}, Additional: []string{}},
	}}
	before, err := before.CompileFacets(false)
	if err != nil {
		t.Fatal(err)
	}
	after, err = after.CompileFacets(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []json.RawMessage{nil, json.RawMessage("1"), json.RawMessage(`"1"`)} {
		card := &Card{CategoryID: "c1", Published: true, Attributes: map[string]json.RawMessage{}}
		if raw != nil {
			card.Attributes["a1"] = raw
		}
		if got, want := after.Check(card), before.Check(card); !reflect.DeepEqual(got, want) {
			t.Fatalf("фильтруемость изменила проверку %s: %+v вместо %+v", raw, got, want)
		}
	}
	if changed := AffectedCategories(before, after); len(changed) != 0 {
		t.Fatalf("изменение фильтров вызвало перепроверку: %v", changed)
	}
}
