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
