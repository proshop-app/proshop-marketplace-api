package taxonomydata

import (
	"slices"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestAudienceNameScopes(t *testing.T) {
	section := func(id string, scoped bool) taxonomy.Category {
		return taxonomy.Category{
			ID:        id,
			Axis:      "audience",
			NameScope: scoped,
			Children: []taxonomy.Category{
				{ID: id + "-leaf", Name: "Одинаковый вид"},
			},
		}
	}
	categories := []taxonomy.Category{section("first", true), section("second", true)}
	if err := auditLeafNames(categories, map[string]string{}); err != nil {
		t.Fatal("повтор между аудиториями должен быть допустим", err)
	}
	categories[0].Children = append(categories[0].Children, taxonomy.Category{
		ID:   "duplicate",
		Name: "одинаковый ВИД",
	})
	if err := auditLeafNames(categories, map[string]string{}); err == nil {
		t.Fatal("повтор внутри одной аудитории пропущен")
	}
	categories = []taxonomy.Category{section("first", false), section("second", false)}
	if err := auditLeafNames(categories, map[string]string{}); err == nil {
		t.Fatal("общие области не должны ослаблять глобальную уникальность")
	}
}

func TestBookGenresAndAudienceDataRemainIndependent(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	fields := release.Definition().Fields()
	genre := fields["book_genre"]
	if genre.Dictionary != "book_genre" || genre.Description != "Жанр произведения." {
		t.Fatal("потерян смысл или словарь жанра произведения")
	}
	var walk func([]taxonomy.Category, bool, bool)
	walk = func(categories []taxonomy.Category, books, audience bool) {
		for _, category := range categories {
			inBooks := books || category.ID == "books"
			inAudience := audience || category.NameScope
			if len(category.Children) != 0 {
				walk(category.Children, inBooks, inAudience)
				continue
			}
			schema := release.Schemas[category.Schema]
			required := []string{}
			if inBooks {
				required = append(required, "book_genre")
			}
			if inAudience {
				required = append(required, "target_gender", "audience_age_group")
			}
			for _, code := range required {
				if !slices.Contains(schema.Attributes, code) || !slices.Contains(schema.Optional, code) ||
					fields[code].Dictionary == "" {
					t.Errorf("%s: независимое необязательное словарное поле потеряно: %s", category.ID, code)
				}
			}
		}
	}
	walk(release.Categories, false, false)
}

func TestSpecialFootwearHasOnePlace(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	definition := release.Definition()
	codes := []string{
		"football-boots",
		"safety-footwear",
		"jazz-shoes",
		"pointe-shoes",
		"ballet-slippers",
		"wading-boots",
		"cheshki",
		"running-sneakers",
		"training-sneakers",
		"tennis-shoes",
		"boxing-shoes",
		"wrestling-shoes",
		"hiking-boots",
		"combat-boots",
		"water-shoes",
		"climbing-shoes",
		"ballroom-shoes",
	}
	for _, code := range codes {
		leaf, path, found := definition.Category("shoes." + code)
		if !found || len(leaf.Children) != 0 || !slices.Contains(path, "shoes.special") {
			t.Errorf("специальный вид потерян или вне общего раздела: %s", code)
		}
	}
	var walk func([]taxonomy.Category, string)
	walk = func(categories []taxonomy.Category, forbidden string) {
		for _, category := range categories {
			if category.ID == "shoes.special" {
				continue
			}
			if len(category.Children) == 0 && category.Name == forbidden {
				t.Errorf("специальный вид продублирован в аудитории: %s", category.ID)
			}
			walk(category.Children, forbidden)
		}
	}
	for _, code := range codes {
		leaf, _, _ := definition.Category("shoes." + code)
		for _, root := range release.Categories {
			if root.ID != "shoes" {
				continue
			}
			for _, section := range root.Children {
				if section.ID != "shoes.special" {
					walk(section.Children, leaf.Name)
				}
			}
		}
	}

	for _, id := range []string{"sports.alpine-ski-boots", "sports.snowboard-boots", "outdoors.roller-skates"} {
		if _, _, found := definition.Category(id); !found {
			t.Errorf("потеряна обувь с креплением к снаряду: %s", id)
		}
	}
}

func TestChildrenHaveFullApplicableFootwear(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	definition := release.Definition()
	if _, _, found := definition.Category("shoes.children-common"); found {
		t.Fatal("общая детская обувь возвращена вместо полных аудиторий")
	}
	for _, audience := range []string{"girls", "boys"} {
		for _, kind := range []string{"boots", "tall-boots", "sneakers", "trainers", "sandals", "slippers"} {
			id := "shoes." + audience + "-" + kind
			leaf, path, found := definition.Category(id)
			if !found || len(leaf.Children) != 0 || !slices.Contains(path, "shoes."+audience) {
				t.Errorf("потерян повседневный детский вид: %s", id)
			}
		}
	}
	for id, reason := range singleChildAudienceExceptions {
		category, _, found := definition.Category(id)
		if !found || len(category.Children) != 1 || !category.NameScope || reason == "" {
			t.Errorf("просроченное или необоснованное исключение группы: %s", id)
		}
	}
}

func TestCommonKindCannotAlsoBeScoped(t *testing.T) {
	leaf := taxonomy.Category{ID: "common", Name: "Вид товара"}
	roots := []taxonomy.Category{{
		ID: "root",
		Children: []taxonomy.Category{
			leaf,
			{ID: "audience", NameScope: true, Children: []taxonomy.Category{leaf}},
		},
	}}
	if err := auditAudiencePlacement(roots); err == nil {
		t.Fatal("общая копия аудиторного вида не обнаружена")
	}
	roots[0].Children = roots[0].Children[1:]
	if err := auditAudiencePlacement(roots); err != nil {
		t.Fatal(err)
	}
}
