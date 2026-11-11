package taxonomy_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestSearchTermsValidation(t *testing.T) {
	for name, terms := range map[string][]string{
		"пустой список": nil,
		"граница":       {strings.Repeat("я", 128)},
		"несколько":     {"значение", "другое значение"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := taxonomy.ValidateSearchTerms(terms); err != nil {
				t.Fatal(err)
			}
		})
	}
	for name, terms := range map[string][]string{
		"пустое":        {""},
		"начало":        {" значение"},
		"конец":         {"значение "},
		"пробелы":       {"два  значения"},
		"особый пробел": {"два\u00a0значения"},
		"BOM":           {"\ufeffзначение"},
		"контроль":      {"зна\u0000чение"},
		"NEL":           {"два\u0085значения"},
		"длинное":       {strings.Repeat("я", 129)},
		"дубли":         {"Значение", "значение"},
		"ё":             {"ёж", "ЕЖ"},
		"кодировка":     {string([]byte{0xff})},
		"количество":    {"1", "2", "3", "4", "5", "6", "7", "8", "9"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := taxonomy.ValidateSearchTerms(terms); !errors.Is(err, taxonomy.ErrInvalid) {
				t.Fatalf("некорректные выражения приняты: %v", err)
			}
		})
	}
}

func TestSearchTermsDefinitionBounds(t *testing.T) {
	for name, mutate := range map[string]func(*taxonomy.Definition){
		"число": func(d *taxonomy.Definition) {
			d.Attributes[1].SearchTermsByValue = map[string][]string{"1": {"значение"}}
		},
		"строка": func(d *taxonomy.Definition) {
			d.Attributes[3].SearchTermsByValue = map[string][]string{"v1": {"значение"}}
		},
		"неизвестное значение": func(d *taxonomy.Definition) {
			d.Attributes[0].SearchTermsByValue = map[string][]string{"v3": {"значение"}}
		},
		"псевдоним boolean": func(d *taxonomy.Definition) {
			d.Attributes[2].SearchTermsByValue = map[string][]string{"True": {"значение"}}
		},
		"пустой ключ": func(d *taxonomy.Definition) {
			d.Attributes[0].SearchTermsByValue = map[string][]string{"": nil}
		},
		"пустое выражение": func(d *taxonomy.Definition) {
			d.Attributes[0].SearchTermsByValue = map[string][]string{"v1": {""}}
		},
		"null вместо списка": func(d *taxonomy.Definition) {
			d.Attributes[0].SearchTermsByValue = map[string][]string{"v1": nil}
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := definition()
			mutate(&d)
			if err := d.Validate(); !errors.Is(err, taxonomy.ErrInvalid) {
				t.Fatalf("некорректные метаданные приняты: %v", err)
			}
		})
	}
}

func TestPublicationSearchTermsFollowActualValidatedValues(t *testing.T) {
	d := definition()
	d.Attributes[0].SearchTermsByValue = map[string][]string{"v1": {"первый"}, "v2": {"второй"}}
	d.Attributes[2].SearchTermsByValue = map[string][]string{"true": {"включённый"}, "false": {"выключенный"}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	c := card()
	c.Attributes["a3"] = json.RawMessage(" false ")
	p := d.Check(c)
	want := map[string][]string{"a1": {"первый"}, "a3": {"выключенный"}}
	if !p.Published || !reflect.DeepEqual(p.AttributeSearchTerms, want) {
		t.Fatalf("неверные выражения: %+v", p)
	}
	p.AttributeSearchTerms["a1"][0] = "изменённый"
	if d.Attributes[0].SearchTermsByValue["v1"][0] != "первый" {
		t.Fatal("публикация меняет метаданные")
	}
	c.Attributes["a1"] = json.RawMessage(`"v2"`)
	c.Attributes["a3"] = json.RawMessage(`true`)
	want = map[string][]string{"a1": {"второй"}, "a3": {"включённый"}}
	if p = d.Check(c); !reflect.DeepEqual(p.AttributeSearchTerms, want) {
		t.Fatalf("старые выражения не заменены: %+v", p)
	}
	delete(c.Attributes, "a3")
	if p = d.Check(c); !p.Published || len(p.AttributeSearchTerms) != 1 {
		t.Fatalf("отсутствующее необязательное значение получило выражения: %+v", p)
	}
	c.Attributes["a3"] = json.RawMessage(`"true"`)
	if p = d.Check(c); p.Published || len(p.AttributeSearchTerms) != 0 {
		t.Fatalf("неверный тип публикует выражения: %+v", p)
	}
	c = card()
	c.Published = false
	if p = d.Check(c); len(p.AttributeSearchTerms) != 0 {
		t.Fatalf("снятая публикация хранит выражения: %+v", p)
	}
	d.Attributes[2].SearchTermsByValue["false"] = []string{}
	if p = d.Check(card()); !p.Published || len(p.AttributeSearchTerms) != 1 {
		t.Fatalf("пустой список не отключил выражения: %+v", p)
	}
}
