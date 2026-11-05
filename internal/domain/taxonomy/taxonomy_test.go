package taxonomy_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func definition() taxonomy.Definition {
	return taxonomy.Definition{
		Attributes: []taxonomy.Attribute{
			{
				Code:       "a1",
				Name:       "Атрибут 1",
				Type:       "enum",
				Values:     []string{"v1", "v2"},
				Facet:      true,
				Dictionary: "a1",
			},
			{
				Code:  "a2",
				Name:  "Атрибут 2",
				Type:  "number",
				Unit:  "ед.",
				Facet: true,
			},
			{Code: "a3", Name: "Атрибут 3", Type: "boolean"},
			{Code: "a4", Name: "Атрибут 4", Type: "string"},
		},
		Schemas: map[string]taxonomy.Schema{
			"s1": {
				Attributes: []string{"a1", "a2", "a3", "a4"},
				Optional:   []string{"a3", "a4"},
			},
		},
		Version: 1,
		Categories: []taxonomy.Category{
			{
				ID:     "c1",
				Slug:   "c1",
				Name:   "Категория",
				Schema: "s1",
			},
		},
	}
}

func card() *taxonomy.Card {
	return &taxonomy.Card{
		CategoryID: "c1",
		Published:  true,
		Attributes: map[string]json.RawMessage{
			"a1": json.RawMessage(`"v1"`),
			"a2": json.RawMessage(`1.25`),
			"a3": json.RawMessage(`false`),
			"a4": json.RawMessage(`"Значение"`),
		},
	}
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
		{
			"категория",
			func(c *taxonomy.Card) { c.CategoryID = "missing" },
			"taxonomy.category_unknown",
			"",
		},
		{
			"неизвестное поле",
			func(c *taxonomy.Card) { c.Attributes["unknown"] = json.RawMessage(`1`) },
			"taxonomy.attribute_unknown",
			"unknown",
		},
		{
			"обязательное",
			func(c *taxonomy.Card) { delete(c.Attributes, "a1") },
			"taxonomy.required",
			"a1",
		},
		{
			"словарь",
			func(c *taxonomy.Card) { c.Attributes["a1"] = json.RawMessage(`"v3"`) },
			"taxonomy.enum_value",
			"a1",
		},
		{
			"тип enum",
			func(c *taxonomy.Card) { c.Attributes["a1"] = json.RawMessage(`true`) },
			"taxonomy.type",
			"a1",
		},
		{
			"тип number",
			func(c *taxonomy.Card) { c.Attributes["a2"] = json.RawMessage(`"1"`) },
			"taxonomy.type",
			"a2",
		},
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
		{
			"тип string",
			func(c *taxonomy.Card) { c.Attributes["a4"] = json.RawMessage(`[]`) },
			"taxonomy.type",
			"a4",
		},
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
		{
			"снята публикация",
			func(c *taxonomy.Card) { c.Published = false },
			"taxonomy.unpublished",
			"",
		},
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
		"код пути":         func(d *taxonomy.Definition) { d.Attributes[0].Code = "a.b" },
		"тип":              func(d *taxonomy.Definition) { d.Attributes[0].Type = "array" },
		"пустой словарь":   func(d *taxonomy.Definition) { d.Attributes[0].Dictionary = "" },
		"повтор значения": func(d *taxonomy.Definition) {
			d.Attributes[0].Values = []string{"v1", "v1"}
		},
		"единица строки": func(d *taxonomy.Definition) { d.Attributes[3].Unit = "ед." },
		"словарь числа":  func(d *taxonomy.Definition) { d.Attributes[1].Values = []string{"v1"} },
		"повтор кода": func(d *taxonomy.Definition) {
			d.Attributes = append(d.Attributes, d.Attributes[0])
		},
		"повтор глобального кода": func(d *taxonomy.Definition) {
			d.Attributes = append(d.Attributes, taxonomy.Attribute{Code: "a1", Name: "Атрибут", Type: "boolean"})
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
	next.Categories[0].Schema = ""
	next.Categories[0].Children = []taxonomy.Category{{ID: "c2", Slug: "c2", Name: "Категория 2", Schema: "s1"}}
	if err := next.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, path, ok := next.Category("c2"); !ok || len(path) != 2 {
		t.Fatalf("%v %v", path, ok)
	}
	if err := next.CheckEvolution(old); err != nil {
		t.Fatal(err)
	}
	next.Attributes[1].Type = "string"
	if err := next.CheckEvolution(old); !errors.Is(err, taxonomy.ErrConflict) {
		t.Fatal(err)
	}
	next = definition()
	next.Attributes = next.Attributes[1:]
	if err := next.CheckEvolution(old); !errors.Is(err, taxonomy.ErrConflict) {
		t.Fatal(err)
	}
}

func TestCategoryNamesAndSlugs(t *testing.T) {
	cases := map[string]func(*taxonomy.Definition){
		"пустой адрес":     func(d *taxonomy.Definition) { d.Categories[0].Slug = "" },
		"формат адреса":    func(d *taxonomy.Definition) { d.Categories[0].Slug = "Bad_slug" },
		"длинный адрес":    func(d *taxonomy.Definition) { d.Categories[0].Slug = strings.Repeat("a", 129) },
		"пробельный title": func(d *taxonomy.Definition) { d.Categories[0].Title = " \t" },
		"длинный title":    func(d *taxonomy.Definition) { d.Categories[0].Title = strings.Repeat("я", 257) },
		"title равен name": func(d *taxonomy.Definition) { d.Categories[0].Title = d.Categories[0].Name },
		"повтор адреса во вложенном узле": func(d *taxonomy.Definition) {
			d.Categories[0].Children = []taxonomy.Category{{ID: "c2", Name: "Другая", Slug: "c1"}}
		},
		"имена соседей при разных title": func(d *taxonomy.Definition) {
			d.Categories = append(d.Categories, taxonomy.Category{
				ID:    "c2",
				Name:  d.Categories[0].Name,
				Title: "Самостоятельное имя",
				Slug:  "c2",
			})
		},
		"полное имя совпало с name другого узла": func(d *taxonomy.Definition) {
			d.Categories[0].Children = []taxonomy.Category{
				{
					ID:    "c2",
					Name:  "Другая",
					Title: d.Categories[0].Name,
					Slug:  "c2",
				},
			}
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
	d := definition()
	if got := d.Check(card()).CategoryName; got != d.Categories[0].Name {
		t.Fatal(got)
	}
	d.Categories[0].Title = strings.Repeat("я", 256)
	d.Categories[0].Slug = strings.Repeat("a", 128)
	d.Categories[0].Schema = ""
	d.Categories[0].Children = []taxonomy.Category{{ID: "c2", Name: d.Categories[0].Name, Slug: "c2", Schema: "s1"}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := d.Check(card()).CategoryName; got != d.Categories[0].Title {
		t.Fatal(got)
	}
}

func TestSlugEvolution(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*taxonomy.Definition, *taxonomy.Definition)
		conflict bool
	}{
		{
			"первое присвоение",
			func(old, next *taxonomy.Definition) { old.Categories[0].Slug = "" },
			false,
		},
		{
			"правка имени",
			func(old, next *taxonomy.Definition) { next.Categories[0].Title = "Новое имя" },
			false,
		},
		{
			"смена адреса",
			func(old, next *taxonomy.Definition) { next.Categories[0].Slug = "new" },
			true,
		},
		{
			"откат без адреса",
			func(old, next *taxonomy.Definition) { next.Categories[0].Slug = "" },
			true,
		},
		{
			"передача адреса",
			func(old, next *taxonomy.Definition) { next.Categories[0].ID = "c2" },
			true,
		},
		{
			"удаление узла",
			func(old, next *taxonomy.Definition) {
				old.Categories[0].Children = []taxonomy.Category{{ID: "c2", Name: "Другая", Slug: "c2"}}
			},
			false,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			old, next := definition(), definition()
			test.mutate(&old, &next)
			err := next.CheckEvolution(old)
			if errors.Is(err, taxonomy.ErrConflict) != test.conflict || (!test.conflict && err != nil) {
				t.Fatalf("%v", err)
			}
			if test.conflict && !strings.Contains(err.Error(), next.Categories[0].ID) {
				t.Fatalf("нет id в конфликте: %v", err)
			}
		})
	}
}

func TestNormalizedReferences(t *testing.T) {
	cases := map[string]func(*taxonomy.Definition){
		"лист без схемы":    func(d *taxonomy.Definition) { d.Categories[0].Schema = "" },
		"неизвестная схема": func(d *taxonomy.Definition) { d.Categories[0].Schema = "missing" },
		"пустая схема":      func(d *taxonomy.Definition) { d.Schemas["s1"] = taxonomy.Schema{} },
		"неизвестное поле": func(d *taxonomy.Definition) {
			d.Schemas["s1"] = taxonomy.Schema{Attributes: []string{"missing"}}
		},
		"повтор поля": func(d *taxonomy.Definition) {
			d.Schemas["s1"] = taxonomy.Schema{Attributes: []string{"a1", "a1"}}
		},
		"optional вне схемы": func(d *taxonomy.Definition) {
			d.Schemas["s1"] = taxonomy.Schema{Attributes: []string{"a1"}, Optional: []string{"a2"}}
		},
		"повтор optional": func(d *taxonomy.Definition) {
			d.Schemas["s1"] = taxonomy.Schema{Attributes: []string{"a1"}, Optional: []string{"a1", "a1"}}
		},
		"словарь числа": func(d *taxonomy.Definition) { d.Attributes[1].Dictionary = "a1" },
		"метаданные строки": func(d *taxonomy.Definition) {
			d.Attributes[3].ValuesMeta = map[string]taxonomy.ValueMeta{"v1": {Hex: "#FFFFFF"}}
		},
		"неверный цвет": func(d *taxonomy.Definition) {
			d.Attributes[0].ValuesMeta = map[string]taxonomy.ValueMeta{"v1": {Hex: "white"}}
		},
		"чужое значение": func(d *taxonomy.Definition) {
			d.Attributes[0].ValuesMeta = map[string]taxonomy.ValueMeta{"other": {Hex: "#FFFFFF"}}
		},
		"схема раздела": func(d *taxonomy.Definition) {
			d.Categories[0].Children = []taxonomy.Category{{ID: "c2", Name: "Другая", Slug: "c2", Schema: "s1"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := definition()
			mutate(&d)
			if !errors.Is(d.Validate(), taxonomy.ErrInvalid) {
				t.Fatal("неверная ссылка принята")
			}
		})
	}
	d := definition()
	d.Attributes[0].Values = nil
	d.Attributes[0].ValuesMeta = map[string]taxonomy.ValueMeta{"v1": {Hex: "#FFFFFF"}}
	if err := d.Validate(); err != nil {
		t.Fatal("структурная проверка требует реестр", err)
	}
	old := definition()
	next := definition()
	next.Attributes[0].Dictionary = "d2"
	if !errors.Is(next.CheckEvolution(old), taxonomy.ErrConflict) {
		t.Fatal("разрешена смена словаря")
	}
}

func TestAssembledSchemaUsesOrderAndOptional(t *testing.T) {
	d := definition()
	d.Schemas["s1"] = taxonomy.Schema{Attributes: []string{"a2", "a1"}, Optional: []string{"a2"}}
	category, _, ok := d.Category("c1")
	if !ok || len(category.Attributes) != 2 || category.Attributes[0].Code != "a2" ||
		category.Attributes[0].Required || !category.Attributes[1].Required ||
		len(category.Attributes[1].Values) != 2 {
		t.Fatal(category)
	}
}
