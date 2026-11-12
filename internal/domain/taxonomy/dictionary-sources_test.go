package taxonomy_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

type sourcesFixture struct {
	Version           int64                          `json:"version"`
	DictionarySources map[string]map[string][]string `json:"dictionarySources"`
}

func TestDictionarySourcesSharedFixture(t *testing.T) {
	fixture := bindingFixture(t)
	expected := map[string]map[string][]string{}
	for _, test := range fixture.Cases {
		if test.CategoryID == "" || len(test.Expected) == 0 {
			continue
		}
		if expected[test.CategoryID] == nil {
			expected[test.CategoryID] = map[string][]string{}
		}
		expected[test.CategoryID][test.AttributeCode] = test.Expected
	}

	actual := fixture.Definition.DictionarySources()
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("карта не совпадает с общим примером: got=%v expected=%v", actual, expected)
	}

	raw, err := os.ReadFile("../../../docs/api/examples/dictionary-sources.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var response sourcesFixture
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.Version != fixture.Definition.Version || !reflect.DeepEqual(response.DictionarySources, actual) {
		t.Fatal("пример ответа расходится со свёрткой", response)
	}
}

func TestDictionarySourcesOmitsUnusedFieldsAndNonStringAttributes(t *testing.T) {
	d := bindingFixture(t).Definition
	d.Attributes[2].Type = "enum"
	d.Attributes[2].Dictionary = "d6"
	d.Attributes = append(d.Attributes, taxonomy.Attribute{
		Code:       "a4",
		Name:       "Поле 4",
		Type:       "string",
		Dictionary: "d7",
	})
	d.Schemas["s2"] = taxonomy.Schema{Attributes: []string{"a2"}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}

	sources := d.DictionarySources()
	for _, category := range []string{"c1.a.y", "c2"} {
		if _, exists := sources[category]; exists {
			t.Fatal("раздел без пар присутствует в карте", category, sources[category])
		}
	}
	for category, fields := range sources {
		for _, code := range []string{"a3", "a4"} {
			if _, exists := fields[code]; exists {
				t.Fatal("лишний атрибут в карте", category, code, fields)
			}
		}
	}
}

func TestDictionarySourcesEmptyObject(t *testing.T) {
	definitions := []taxonomy.Definition{
		{},
		{
			Attributes: []taxonomy.Attribute{{Code: "a1", Type: "string"}},
			Schemas:    map[string]taxonomy.Schema{"s1": {Attributes: []string{"a1"}}},
			Categories: []taxonomy.Category{{ID: "c1", Schema: "s1"}},
		},
	}
	for _, d := range definitions {
		sources := d.DictionarySources()
		if sources == nil || len(sources) != 0 {
			t.Fatal("нужен пустой объект без разделов", sources)
		}
	}
}
