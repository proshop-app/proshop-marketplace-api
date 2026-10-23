package taxonomy_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func definition() taxonomy.Definition {
	return taxonomy.Definition{Version: 1, Categories: []taxonomy.Category{{
		ID:   "c1",
		Name: "Категория",
		Attributes: []taxonomy.Attribute{
			{
				Code:     "a1",
				Name:     "Атрибут 1",
				Type:     "enum",
				Required: true,
				Values:   []string{"v1", "v2"},
				Facet:    true,
			},
			{Code: "a2", Name: "Атрибут 2", Type: "number", Unit: "ед.", Required: true, Facet: true},
			{Code: "a3", Name: "Атрибут 3", Type: "boolean"},
			{Code: "a4", Name: "Атрибут 4", Type: "string"},
		},
	}}}
}

func card() *taxonomy.Card {
	return &taxonomy.Card{CategoryID: "c1", Published: true, Attributes: map[string]json.RawMessage{
		"a1": json.RawMessage(`"v1"`),
		"a2": json.RawMessage(`1.25`),
		"a3": json.RawMessage(`false`),
		"a4": json.RawMessage(`"Значение"`),
	}}
}

func TestValidation(t *testing.T) {
	d := definition()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	p := d.Check(card())
	if !p.Published || len(p.Attributes) != 4 || p.CategoryPath[0] != "c1" || p.Version != 1 {
		t.Fatalf("%+v", p)
	}
	tests := []struct {
		name      string
		mutate    func(*taxonomy.Card)
		issue     string
		attribute string
	}{
		{"категория", func(c *taxonomy.Card) { c.CategoryID = "missing" }, "taxonomy.category_unknown", ""},
		{
			"неизвестное поле",
			func(c *taxonomy.Card) { c.Attributes["unknown"] = json.RawMessage(`1`) },
			"taxonomy.attribute_unknown",
			"unknown",
		},
		{"обязательное", func(c *taxonomy.Card) { delete(c.Attributes, "a1") }, "taxonomy.required", "a1"},
		{
			"словарь",
			func(c *taxonomy.Card) { c.Attributes["a1"] = json.RawMessage(`"v3"`) },
			"taxonomy.enum_value",
			"a1",
		},
		{"тип enum", func(c *taxonomy.Card) { c.Attributes["a1"] = json.RawMessage(`true`) }, "taxonomy.type", "a1"},
		{"тип number", func(c *taxonomy.Card) { c.Attributes["a2"] = json.RawMessage(`"1"`) }, "taxonomy.type", "a2"},
		{
			"переполнение",
			func(c *taxonomy.Card) { c.Attributes["a2"] = json.RawMessage(`1e309`) },
			"taxonomy.type",
			"a2",
		},
		{
			"тип boolean",
			func(c *taxonomy.Card) { c.Attributes["a3"] = json.RawMessage(`"false"`) },
			"taxonomy.type",
			"a3",
		},
		{"тип string", func(c *taxonomy.Card) { c.Attributes["a4"] = json.RawMessage(`[]`) }, "taxonomy.type", "a4"},
		{
			"null boolean",
			func(c *taxonomy.Card) { c.Attributes["a3"] = json.RawMessage(` null `) },
			"taxonomy.type",
			"a3",
		},
		{
			"пустая строка",
			func(c *taxonomy.Card) { c.Attributes["a4"] = json.RawMessage(`" "`) },
			"taxonomy.type",
			"a4",
		},
		{"снята публикация", func(c *taxonomy.Card) { c.Published = false }, "taxonomy.unpublished", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := card()
			test.mutate(c)
			p := d.Check(c)
			if p.Published || len(p.Attributes) != 0 {
				t.Fatalf("ошибка публикуется: %+v", p)
			}
			for _, issue := range p.Issues {
				if issue.Code == test.issue && issue.Attribute == test.attribute {
					return
				}
			}
			t.Fatalf("нет ошибки %s: %+v", test.issue, p)
		})
	}
	if p := d.Check(nil); p.Published || p.Issues[0].Code != "taxonomy.card_missing" {
		t.Fatalf("%+v", p)
	}
}

func TestInvalidDefinitions(t *testing.T) {
	cases := map[string]func(*taxonomy.Definition){
		"версия":           func(d *taxonomy.Definition) { d.Version = 0 },
		"пустое дерево":    func(d *taxonomy.Definition) { d.Categories = nil },
		"повтор категории": func(d *taxonomy.Definition) { d.Categories = append(d.Categories, d.Categories[0]) },
		"код пути":         func(d *taxonomy.Definition) { d.Categories[0].Attributes[0].Code = "a.b" },
		"тип":              func(d *taxonomy.Definition) { d.Categories[0].Attributes[0].Type = "array" },
		"пустой словарь":   func(d *taxonomy.Definition) { d.Categories[0].Attributes[0].Values = nil },
		"повтор значения": func(d *taxonomy.Definition) {
			d.Categories[0].Attributes[0].Values = []string{"v1", "v1"}
		},
		"единица строки": func(d *taxonomy.Definition) { d.Categories[0].Attributes[3].Unit = "ед." },
		"словарь числа":  func(d *taxonomy.Definition) { d.Categories[0].Attributes[1].Values = []string{"v1"} },
		"повтор кода": func(d *taxonomy.Definition) {
			d.Categories[0].Attributes = append(d.Categories[0].Attributes, d.Categories[0].Attributes[0])
		},
		"глобальный тип": func(d *taxonomy.Definition) {
			d.Categories = append(d.Categories, taxonomy.Category{
				ID:         "c2",
				Name:       "Категория 2",
				Attributes: []taxonomy.Attribute{{Code: "a1", Name: "Атрибут", Type: "boolean"}},
			})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := definition()
			mutate(&d)
			if err := d.Validate(); !errors.Is(err, taxonomy.ErrInvalid) {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestEvolutionAndTree(t *testing.T) {
	old := definition()
	next := definition()
	next.Version++
	next.Categories[0].Children = []taxonomy.Category{{ID: "c2", Name: "Категория 2"}}
	if err := next.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, path, ok := next.Category("c2"); !ok || len(path) != 2 {
		t.Fatalf("%v %v", path, ok)
	}
	if err := next.CheckEvolution(old); err != nil {
		t.Fatal(err)
	}
	next.Categories[0].Attributes[1].Type = "string"
	if err := next.CheckEvolution(old); !errors.Is(err, taxonomy.ErrConflict) {
		t.Fatal(err)
	}
	next = definition()
	next.Categories[0].Attributes = next.Categories[0].Attributes[1:]
	if err := next.CheckEvolution(old); !errors.Is(err, taxonomy.ErrConflict) {
		t.Fatal(err)
	}
}
