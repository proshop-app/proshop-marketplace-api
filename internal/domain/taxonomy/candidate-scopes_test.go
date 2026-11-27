package taxonomy_test

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestCandidateScopesSharedResolution(t *testing.T) {
	fixture := bindingFixture(t)
	for _, test := range fixture.Cases {
		for i := 0; i <= 5; i++ {
			code := fmt.Sprintf("d%d", i)
			matches := 0
			for _, scope := range fixture.Definition.CandidateScopes(code, reference.KindOpen) {
				contains := candidateScopeContains(fixture.Definition, scope, test.CategoryID)
				if scope.Attribute != test.AttributeCode || !contains {
					continue
				}
				matches++
				if !reflect.DeepEqual(scope.Dictionaries, test.Expected) {
					t.Fatal("область отличается от общей свёртки", scope, test)
				}
			}
			expected := 0
			if slices.Contains(test.Expected, code) {
				expected = 1
			}
			if matches != expected {
				t.Fatalf(
					"%s/%s/%s: областей %d, нужно %d",
					test.CategoryID,
					test.AttributeCode,
					code,
					matches,
					expected,
				)
			}
		}
	}
}

func TestCandidateScopesDefaultAndNestedReturn(t *testing.T) {
	d := bindingFixture(t).Definition
	d.DictionaryBindings = nil
	scopes := d.CandidateScopes("d0", reference.KindOpen)
	expected := []taxonomy.CandidateScope{{
		Attribute:         "a1",
		Categories:        []string{},
		ExcludeCategories: []string{},
		Dictionaries:      []string{"d0"},
	}}
	if !reflect.DeepEqual(scopes, expected) {
		t.Fatal("глобальный словарь с другим кодом", scopes)
	}
	d.DictionaryBindings = []taxonomy.DictionaryBinding{
		{CategoryID: "c1", AttributeCode: "a1", Mode: "replace", Dictionaries: []string{"d1"}},
		{CategoryID: "c1.a", AttributeCode: "a1", Mode: "replace", Dictionaries: []string{"d0"}},
	}
	expected[0].ExcludeCategories = []string{"c1"}
	expected = append(expected, taxonomy.CandidateScope{
		Attribute:         "a1",
		Categories:        []string{"c1.a"},
		ExcludeCategories: []string{},
		Dictionaries:      []string{"d0"},
	})
	if got := d.CandidateScopes("d0", reference.KindOpen); !reflect.DeepEqual(got, expected) {
		t.Fatal("replace и вложенный возврат", got)
	}
	if len(d.CandidateScopes("d0", reference.KindClosed)) != 0 {
		t.Fatal("закрытый словарь получил области")
	}
	d.Attributes[0].Type = "enum"
	if len(d.CandidateScopes("d0", reference.KindOpen)) != 0 {
		t.Fatal("нестроковый атрибут получил области")
	}
	if len((taxonomy.Definition{}).CandidateScopes("d0", reference.KindOpen)) != 0 {
		t.Fatal("пустая таксономия получила области")
	}
}

func candidateScopeContains(d taxonomy.Definition, scope taxonomy.CandidateScope, category string) bool {
	var path []string
	if category != "" {
		_, path, _ = d.Category(category)
	}
	for _, excluded := range scope.ExcludeCategories {
		if slices.Contains(path, excluded) {
			return false
		}
	}
	if len(scope.Categories) == 0 {
		return true
	}
	for _, root := range scope.Categories {
		if slices.Contains(path, root) {
			return true
		}
	}
	return false
}

// Раскладка за один обход даёт каждому словарю те же области, что отдельный расчёт,
// в котором он получатель кандидатов.
func TestCandidateScopesByReceiverMatchesPerDictionary(t *testing.T) {
	fixture := bindingFixture(t)
	byReceiver := fixture.Definition.CandidateScopesByReceiver()
	total := 0
	for i := 0; i <= 5; i++ {
		code := fmt.Sprintf("d%d", i)
		expected := []taxonomy.CandidateScope{}
		for _, scope := range fixture.Definition.CandidateScopes(code, reference.KindOpen) {
			if scope.Dictionaries[len(scope.Dictionaries)-1] == code {
				expected = append(expected, scope)
			}
		}
		got := byReceiver[code]
		if len(got) != len(expected) || (len(got) > 0 && !reflect.DeepEqual(got, expected)) {
			t.Fatalf("словарь %s: области за один обход расходятся: %d против %d", code, len(got), len(expected))
		}
		total += len(got)
	}
	if total == 0 {
		t.Fatal("в образце нет ни одной области с получателем")
	}
	if len((taxonomy.Definition{}).CandidateScopesByReceiver()) != 0 {
		t.Fatal("без версии таксономии областей нет")
	}
}
