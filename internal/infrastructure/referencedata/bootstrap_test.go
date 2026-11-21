package referencedata

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/taxonomydata"
)

type suppliedDictionary struct {
	kind    reference.Kind
	entries []reference.DeliveryEntry
}

// Эти две неизменяемые миграции задают заголовки для старых поставок без kind.
// Читаем SQL, чтобы тест не подтверждал вручную продублированный список словарей.
func migrationDictionaries(t *testing.T) map[string]suppliedDictionary {
	t.Helper()
	result := map[string]suppliedDictionary{}
	pair := regexp.MustCompile(`\(\s*'([a-z][a-z0-9_]*)'\s*,\s*'(open|closed)'`)
	for _, name := range []string{"00027_reference_registry.sql", "00030_reference_dictionaries.sql"} {
		body, err := os.ReadFile("../postgres/migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		up := strings.SplitN(string(body), "-- +goose Down", 2)[0]
		_, inserts, found := strings.Cut(up, "INSERT INTO reference.dictionaries")
		if !found {
			t.Fatalf("не найдена регистрация словарей в %s", name)
		}
		matches := pair.FindAllStringSubmatch(inserts, -1)
		if len(matches) == 0 {
			t.Fatalf("не найдены заголовки словарей в %s", name)
		}
		for _, match := range matches {
			if _, exists := result[match[1]]; exists {
				t.Fatalf("повтор заголовка %s в миграциях", match[1])
			}
			result[match[1]] = suppliedDictionary{kind: reference.Kind(match[2])}
		}
	}
	return result
}

func assertTaxonomyDictionaries(t *testing.T, dictionaries map[string]suppliedDictionary) {
	t.Helper()
	input, err := LoadImport(os.DirFS("../../../deploy/reference"), "bootstrap.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range input.Dictionaries {
		if _, exists := dictionaries[item.Code]; exists {
			t.Fatalf("bootstrap повторяет %s: возможен конфликт с историей старой поставки", item.Code)
		}
		dictionaries[item.Code] = suppliedDictionary{kind: item.Kind, entries: item.Entries}
	}
	release, _, err := taxonomydata.Load()
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{}
	for _, attribute := range release.Attributes {
		if attribute.Dictionary == "" {
			continue
		}
		code := attribute.Dictionary
		required[code] = true
		kind := reference.KindOpen
		if attribute.Type == "enum" {
			kind = reference.KindClosed
		}
		// Поставки Uno регистрируют новые словари отдельно от bootstrap площадки.
		if _, bundled := dictionaries[code]; !bundled {
			continue
		}
		dictionary := requireDictionary(t, dictionaries, code, kind)
		if kind != reference.KindClosed {
			continue
		}
		confirmed := map[string]bool{}
		for _, entry := range dictionary.entries {
			if entry.Status == reference.StatusConfirmed {
				confirmed[entry.Canonical] = true
			}
		}
		if len(confirmed) == 0 {
			t.Errorf("у %s нет confirmed-значений", code)
		}
		for value := range attribute.ValuesMeta {
			if !confirmed[value] {
				t.Errorf("%s.valuesMeta: %s не подтверждено в %s", attribute.Code, value, code)
			}
		}
		for value := range attribute.SearchTermsByValue {
			if !confirmed[value] {
				t.Errorf("%s.searchTermsByValue: %s не подтверждено в %s", attribute.Code, value, code)
			}
		}
	}
	for _, binding := range release.DictionaryBindings {
		for _, code := range binding.Dictionaries {
			required[code] = true
			if _, bundled := dictionaries[code]; bundled {
				requireDictionary(t, dictionaries, code, reference.KindOpen)
			}
		}
	}
	for _, item := range input.Dictionaries {
		if !required[item.Code] {
			t.Errorf("лишний словарь bootstrap: %s", item.Code)
		}
		if item.Kind == reference.KindOpen && len(item.Entries) != 0 {
			t.Errorf("bootstrap не должен подменять внешние значения открытого словаря %s", item.Code)
		}
	}
}

func requireDictionary(
	t *testing.T,
	dictionaries map[string]suppliedDictionary,
	code string,
	kind reference.Kind,
) suppliedDictionary {
	t.Helper()
	dictionary, exists := dictionaries[code]
	if !exists || dictionary.kind != kind {
		t.Fatalf("словарь %s: нужен %s, поставлен %s (есть: %t)", code, kind, dictionary.kind, exists)
	}
	return dictionary
}
