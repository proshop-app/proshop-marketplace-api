package taxonomydata

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// Разные поисковые названия одного товара не должны создавать несколько листьев.
// Область — аудитория либо всё дерево вне аудиторий, как у проверки имён.
var reviewedSynonymNames = [][]string{
	{"Сумки через плечо", "Кросс-боди", "Сумки кросс-боди"},
	{"Броши и декоративные булавки", "Броши", "Декоративные булавки"},
	{"Кулоны и подвески", "Медальоны", "Кулоны", "Подвески"},
	{"Визитницы", "Футляры для визиток"},
	{"Маршрутизаторы", "Роутеры"},
	{"USB-концентраторы", "USB-хабы"},
	{"Внешние аккумуляторы", "Пауэрбанки"},
	{"Геймпады", "Игровые контроллеры"},
	{"МФУ", "Многофункциональные устройства"},
	{"Канцелярские зажимы для бумаг", "Биндеры"},
	{"Кофры для хранения", "Тканевые короба для хранения"},
}

func TestReviewedSynonymsHaveOneLeafPerScope(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	var walk func([]taxonomy.Category, string)
	walk = func(nodes []taxonomy.Category, scope string) {
		for _, node := range nodes {
			next := scope
			if node.NameScope {
				next = node.ID
			}
			if len(node.Children) > 0 {
				walk(node.Children, next)
				continue
			}
			for _, names := range reviewedSynonymNames {
				for _, name := range names {
					if normalizedName(node.Name) != normalizedName(name) {
						continue
					}
					key := next + ":" + names[0]
					if previous := seen[key]; previous != "" {
						t.Errorf("синонимы в двух листьях: %s, %s", previous, node.ID)
					}
					seen[key] = node.ID
				}
			}
		}
	}
	walk(release.Categories, "")
}

func TestSequentialImportSchemasOnlyPreserveRelease9References(t *testing.T) {
	body, err := os.ReadFile("testdata/release-9.json")
	if err != nil {
		t.Fatal(err)
	}
	var previous taxonomy.Definition
	if err := json.Unmarshal(body, &previous); err != nil {
		t.Fatal(err)
	}
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	var walk func([]taxonomy.Category)
	walk = func(nodes []taxonomy.Category) {
		for _, node := range nodes {
			if sequentialImportSchemas[node.Schema] != "" {
				t.Errorf("новый лист использует переходную схему: %s", node.ID)
			}
			walk(node.Children)
		}
	}
	walk(release.Categories)
	for key, reason := range sequentialImportSchemas {
		if reason == "" || len(previous.Schemas[key].Attributes) == 0 || len(release.Schemas[key].Attributes) == 0 {
			t.Errorf("необоснованное исключение совместимости: %s", key)
		}
	}
	for _, id := range []string{
		"furniture.loft-beds",
		"furniture.bunk-beds",
		"furniture.secretaires",
		"furniture.tv-stands",
		"textiles.decorative-cushion-covers",
		"textiles.weighted-blankets",
		"textiles.carpet-rolls",
	} {
		if node, _, ok := release.Definition().Category(id); !ok || node.Schema == "" {
			t.Errorf("потерян проверенный вид мебели/текстиля: %s", id)
		}
	}
}
