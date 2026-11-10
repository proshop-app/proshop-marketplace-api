package taxonomy_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

type resolutionFixture struct {
	Definition taxonomy.Definition `json:"definition"`
	Cases      []resolutionCase    `json:"cases"`
}

type resolutionCase struct {
	CategoryID    string   `json:"categoryId"`
	AttributeCode string   `json:"attributeCode"`
	Expected      []string `json:"expected"`
}

func bindingFixture(t *testing.T) resolutionFixture {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/api/examples/dictionary-bindings-resolution.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture resolutionFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestDictionaryBindingResolutionSharedFixture(t *testing.T) {
	fixture := bindingFixture(t)
	if err := fixture.Definition.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, test := range fixture.Cases {
		actual := fixture.Definition.HintDictionaries(test.CategoryID, test.AttributeCode)
		if !reflect.DeepEqual(actual, test.Expected) {
			t.Fatalf("%s/%s: got=%v expected=%v", test.CategoryID, test.AttributeCode, actual, test.Expected)
		}
	}
}

func TestDictionaryBindingRejectsDanglingDuplicateAndIncompatibleRules(t *testing.T) {
	cases := map[string]func(*taxonomy.Definition){
		"неизвестная категория": func(d *taxonomy.Definition) { d.DictionaryBindings[0].CategoryID = "missing" },
		"неизвестный атрибут":   func(d *taxonomy.Definition) { d.DictionaryBindings[0].AttributeCode = "missing" },
		"число":                 func(d *taxonomy.Definition) { d.DictionaryBindings[0].AttributeCode = "a3" },
		"пустой extend":         func(d *taxonomy.Definition) { d.DictionaryBindings[0].Dictionaries = []string{} },
		"null replace": func(d *taxonomy.Definition) {
			d.DictionaryBindings[0].Mode = taxonomy.BindingReplace
			d.DictionaryBindings[0].Dictionaries = nil
		},
		"неверный режим": func(d *taxonomy.Definition) { d.DictionaryBindings[0].Mode = "append" },
		"повтор словаря": func(d *taxonomy.Definition) {
			d.DictionaryBindings[0].Dictionaries = []string{"d1", "d1"}
		},
		"неверный словарь": func(d *taxonomy.Definition) { d.DictionaryBindings[0].Dictionaries = []string{"../d1"} },
		"повтор области": func(d *taxonomy.Definition) {
			d.DictionaryBindings = append(d.DictionaryBindings, d.DictionaryBindings[0])
		},
		"атрибут вне области": func(d *taxonomy.Definition) {
			d.DictionaryBindings = []taxonomy.DictionaryBinding{
				{CategoryID: "c2", AttributeCode: "a3", Mode: "replace", Dictionaries: []string{}},
			}
		},
		"атрибут больше не используется": func(d *taxonomy.Definition) {
			for key, schema := range d.Schemas {
				schema.Attributes = []string{"a2"}
				d.Schemas[key] = schema
			}
		},
		"слишком много правил": func(d *taxonomy.Definition) {
			d.DictionaryBindings = make([]taxonomy.DictionaryBinding, taxonomy.MaxDictionaryBindings+1)
		},
		"слишком много источников правила": func(d *taxonomy.Definition) {
			d.DictionaryBindings[0].Dictionaries = make([]string, taxonomy.MaxHintDictionaries+1)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := bindingFixture(t).Definition
			mutate(&d)
			if !errors.Is(d.Validate(), taxonomy.ErrInvalid) {
				t.Fatal("неверные правила приняты")
			}
		})
	}
}

func TestGlobalEffectiveHintLimitAppliesEvenWhenRootsReplace(t *testing.T) {
	d := bindingFixture(t).Definition
	codes := []string{}
	for i := 1; i <= taxonomy.MaxHintDictionaries; i++ {
		codes = append(codes, fmt.Sprintf("d%d", i))
	}
	d.DictionaryBindings = []taxonomy.DictionaryBinding{
		{AttributeCode: "a1", Mode: "extend", Dictionaries: codes},
		{CategoryID: "c1", AttributeCode: "a1", Mode: "replace", Dictionaries: []string{}},
		{CategoryID: "c2", AttributeCode: "a1", Mode: "replace", Dictionaries: []string{}},
	}
	if !errors.Is(d.Validate(), taxonomy.ErrInvalid) {
		t.Fatal("глобальные17 источников приняты из-за replace на всех корнях")
	}
	d.DictionaryBindings[0].Dictionaries = codes[:taxonomy.MaxHintDictionaries-1]
	if err := d.Validate(); err != nil {
		t.Fatal("глобальные16 источников отклонены", err)
	}
	d.DictionaryBindings[1].Mode = "extend"
	d.DictionaryBindings[1].Dictionaries = []string{"d17"}
	if !errors.Is(d.Validate(), taxonomy.ErrInvalid) {
		t.Fatal("локальное наследование превысило16 источников")
	}
}
