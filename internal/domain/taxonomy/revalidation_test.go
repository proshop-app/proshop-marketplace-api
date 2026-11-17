package taxonomy_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func revalidationDefinition() taxonomy.Definition {
	return taxonomy.Definition{
		Version: 1,
		Attributes: []taxonomy.Attribute{
			{Code: "a1", Type: "string", Name: "Поле"},
			{Code: "a2", Type: "string", Name: "Другое поле"},
		},
		Schemas: map[string]taxonomy.Schema{
			"s1":     {Attributes: []string{"a1"}},
			"s2":     {Attributes: []string{"a2"}},
			"unused": {Attributes: []string{"a1"}},
		},
		Categories: []taxonomy.Category{
			{
				ID: "root",
				Children: []taxonomy.Category{
					{ID: "c1", Schema: "s1"},
					{ID: "c2", Schema: "s2", Moderation: "auto"},
				},
			},
		},
	}
}

func TestAffectedCategories(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*taxonomy.Definition)
		want   []string
	}{
		{
			name: "только версия",
			change: func(d *taxonomy.Definition) {
				d.Version++
			},
			want: []string{},
		},
		{
			name: "атрибут только одной схемы",
			change: func(d *taxonomy.Definition) {
				d.Attributes[0].Name = "Новое имя"
			},
			want: []string{"c1"},
		},
		{
			name: "неиспользуемая схема",
			change: func(d *taxonomy.Definition) {
				delete(d.Schemas, "unused")
			},
			want: []string{},
		},
		{
			name: "обязательность в схеме",
			change: func(d *taxonomy.Definition) {
				d.Schemas["s1"] = taxonomy.Schema{Attributes: []string{"a1"}, Optional: []string{"a1"}}
			},
			want: []string{"c1"},
		},
		{
			name: "тип атрибута",
			change: func(d *taxonomy.Definition) {
				d.Attributes[0].Type = "enum"
			},
			want: []string{"c1"},
		},
		{
			name: "единица атрибута",
			change: func(d *taxonomy.Definition) {
				d.Attributes[0].Unit = "unit"
			},
			want: []string{"c1"},
		},
		{
			name: "формат атрибута",
			change: func(d *taxonomy.Definition) {
				d.Attributes[0].Format = "isbn"
			},
			want: []string{"c1"},
		},
		{
			name: "закрытые значения",
			change: func(d *taxonomy.Definition) {
				d.Attributes[0].Values = []string{"v1"}
			},
			want: []string{"c1"},
		},
		{
			name: "поисковые выражения",
			change: func(d *taxonomy.Definition) {
				d.Attributes[0].SearchTermsByValue = map[string][]string{"v1": {"word"}}
			},
			want: []string{"c1"},
		},
		{
			name: "накопительные фасеты",
			change: func(d *taxonomy.Definition) {
				d.Attributes[0].FacetValuesByValue = map[string][]string{"v1": {"facet"}}
			},
			want: []string{"c1"},
		},
		{
			name: "удалённый глобальный атрибут",
			change: func(d *taxonomy.Definition) {
				d.Attributes = d.Attributes[1:]
			},
			want: []string{"c1"},
		},
		{
			name: "удалённое поле схемы",
			change: func(d *taxonomy.Definition) {
				d.Schemas["s1"] = taxonomy.Schema{}
			},
			want: []string{"c1"},
		},
		{
			name: "identity модели",
			change: func(d *taxonomy.Definition) {
				d.Schemas["s1"] = taxonomy.Schema{
					Attributes: []string{"a1"},
					Identity:   &taxonomy.SchemaIdentity{Model: []string{"a1"}},
				}
			},
			want: []string{"c1"},
		},
		{
			name: "identity оси",
			change: func(d *taxonomy.Definition) {
				d.Schemas["s1"] = taxonomy.Schema{
					Attributes: []string{"a1"},
					Identity:   &taxonomy.SchemaIdentity{Axes: []string{"a1"}},
				}
			},
			want: []string{"c1"},
		},
		{
			name: "identity точного варианта",
			change: func(d *taxonomy.Definition) {
				d.Schemas["s1"] = taxonomy.Schema{
					Attributes: []string{"a1"},
					Identity:   &taxonomy.SchemaIdentity{Exact: []string{"a1"}},
				}
			},
			want: []string{"c1"},
		},
		{
			name: "атрибут вне схем",
			change: func(d *taxonomy.Definition) {
				d.Attributes = append(d.Attributes, taxonomy.Attribute{Code: "unused", Type: "string"})
			},
			want: []string{},
		},
		{
			name: "глобальные подсказки",
			change: func(d *taxonomy.Definition) {
				d.DictionaryBindings = []taxonomy.DictionaryBinding{
					{
						AttributeCode: "a1",
						Mode:          taxonomy.BindingReplace,
						Dictionaries:  []string{"other"},
					},
				}
			},
			want: []string{},
		},
		{
			name: "подсказки категории",
			change: func(d *taxonomy.Definition) {
				d.DictionaryBindings = []taxonomy.DictionaryBinding{
					{
						CategoryID:    "c1",
						AttributeCode: "a1",
						Mode:          taxonomy.BindingReplace,
						Dictionaries:  []string{"other"},
					},
				}
			},
			want: []string{},
		},
		{
			name: "полное имя категории",
			change: func(d *taxonomy.Definition) {
				d.Categories[0].Children[0].Title = "Новое полное имя"
			},
			want: []string{"c1"},
		},
		{
			name: "другая схема категории",
			change: func(d *taxonomy.Definition) {
				d.Categories[0].Children[0].Schema = "s2"
			},
			want: []string{"c1"},
		},
		{
			name: "добавленный лист",
			change: func(d *taxonomy.Definition) {
				d.Categories = append(d.Categories, taxonomy.Category{ID: "c3", Schema: "s1"})
			},
			want: []string{"c3"},
		},
		{
			name: "удалённый лист",
			change: func(d *taxonomy.Definition) {
				d.Categories[0].Children = d.Categories[0].Children[1:]
			},
			want: []string{"c1"},
		},
		{
			name: "перенос листа",
			change: func(d *taxonomy.Definition) {
				d.Categories = append(d.Categories, d.Categories[0].Children[0])
				d.Categories[0].Children = d.Categories[0].Children[1:]
			},
			want: []string{"c1"},
		},
		{
			name: "наследование модерации",
			change: func(d *taxonomy.Definition) {
				d.Categories[0].Moderation = "forbidden"
			},
			want: []string{"c1", "root"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			previous := revalidationDefinition()
			raw, err := json.Marshal(previous)
			if err != nil {
				t.Fatal(err)
			}
			var next taxonomy.Definition
			if err := json.Unmarshal(raw, &next); err != nil {
				t.Fatal(err)
			}
			test.change(&next)
			if got := taxonomy.AffectedCategories(previous, next); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("категории: %v, ожидались %v", got, test.want)
			}
		})
	}
}

func TestCheckerMatchesDefinitionCheckAndBindingsOnlyChangeHints(t *testing.T) {
	definition := revalidationDefinition()
	checker := definition.Checker()
	cards := []*taxonomy.Card{
		nil,
		{CategoryID: "missing", Published: true},
		{CategoryID: "c1", Published: true, Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`"value"`)}},
		{CategoryID: "c1", Published: false},
		{CategoryID: "c2", Published: true, Attributes: map[string]json.RawMessage{"extra": json.RawMessage(`1`)}},
	}
	for _, card := range cards {
		if !reflect.DeepEqual(definition.Check(card), checker.Check(card)) {
			t.Fatal("контекст изменил результат Check", card)
		}
	}
	before := definition.Check(cards[2])
	definition.DictionaryBindings = []taxonomy.DictionaryBinding{
		{CategoryID: "c1", AttributeCode: "a1", Mode: taxonomy.BindingReplace, Dictionaries: []string{"hint"}},
	}
	if !reflect.DeepEqual(before, definition.Check(cards[2])) || len(definition.HintDictionaries("c1", "a1")) != 1 {
		t.Fatal("привязка изменила допустимые значения вместо источников подсказок")
	}
}
