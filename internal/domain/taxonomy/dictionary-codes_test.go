package taxonomy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestDictionaryReferencesUseReferenceCodeValidation(t *testing.T) {
	cases := []struct {
		name  string
		code  string
		valid bool
	}{
		{name: "1 символ", code: "a", valid: true},
		{name: "31 символ", code: strings.Repeat("a", 31), valid: true},
		{name: "32 символа", code: "a" + strings.Repeat("0_", 15) + "0", valid: true},
		{name: "33 символа", code: strings.Repeat("a", 33)},
		{name: "64 символа", code: strings.Repeat("a", 64)},
		{name: "65 символов", code: strings.Repeat("a", 65)},
		{name: "цифра в начале", code: "1a"},
		{name: "подчёркивание в начале", code: "_a"},
		{name: "верхний регистр", code: "A"},
		{name: "кириллица", code: "а"},
		{name: "дефис", code: "a-b"},
		{name: "пробел", code: "a "},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if actual := reference.ValidCode(test.code); actual != test.valid {
				t.Fatalf("reference.ValidCode(%q) = %v, ожидалось %v", test.code, actual, test.valid)
			}
			for _, target := range []string{"enum", "string", "binding"} {
				t.Run(target, func(t *testing.T) {
					d := definition()
					switch target {
					case "enum":
						d.Attributes[0].Dictionary = test.code
					case "string":
						d.Attributes[3].Dictionary = test.code
					case "binding":
						d.DictionaryBindings = []taxonomy.DictionaryBinding{
							{AttributeCode: "a4", Mode: "extend", Dictionaries: []string{test.code}},
						}
					}
					err := d.Validate()
					if (err == nil) != test.valid {
						t.Fatalf("проверка ссылки %q: %v, ожидалась допустимость %v", test.code, err, test.valid)
					}
					if !test.valid && (!errors.Is(err, taxonomy.ErrInvalid) || !strings.Contains(err.Error(), "32")) {
						t.Fatalf("нужна ошибка валидации с ограничением длины: %v", err)
					}
				})
			}
		})
	}
}

func TestTaxonomyCodeLimitRemains64(t *testing.T) {
	if !taxonomy.ValidCode(strings.Repeat("a", 64)) || taxonomy.ValidCode(strings.Repeat("a", 65)) {
		t.Fatal("ограничение кода атрибута или схемы изменилось вместе с кодом словаря")
	}
}
