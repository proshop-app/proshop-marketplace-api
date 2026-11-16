package taxonomydata

import (
	"slices"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// Устойчивые товарные термины нельзя исправлять ради формального запрета слова.
// Каждое исключение — конкретный узел, без разрешения всей семьи или направления.
var buyerNameTechnicalExceptions = map[string]struct {
	name   string
	reason string
}{
	"computers.cases": {
		name:   "Корпуса компьютеров",
		reason: "Корпус компьютера — самостоятельный товарный термин.",
	},
	"computers.case-fans": {
		name:   "Корпусные вентиляторы",
		reason: "Корпусный вентилятор — термин компьютерного охлаждения.",
	},
	"computers.laptop-cases": {
		name:   "Корпуса для ноутбуков",
		reason: "Корпус ноутбука — название ремонтной запчасти.",
	},
	"food.pasta": {
		name:   "Макаронные изделия",
		reason: "Устойчивое название пищевой группы.",
	},
	"food.sausages": {
		name:   "Колбасные изделия",
		reason: "Устойчивое название пищевой группы.",
	},
	"food.curd-products": {
		name:   "Творожные изделия",
		reason: "Готовые продукты отличаются от творога.",
	},
	"food.family-tobacco": {
		name:   "Табачные изделия",
		reason: "Обычное название магазинного раздела.",
	},
	"household.leather-care": {
		name:   "Средства ухода за кожаными изделиями",
		reason: "Отличает уход за вещами от косметики для кожи человека.",
	},
}

var buyerNameTechnicalStems = []string{
	"конструкц",
	"издел",
	"корпус",
	"принадлеж",
	"фотополимеризац",
	"общего назначения",
	"без фиксации",
	"без наполнителя",
}

func TestBuyerLanguageAcrossAllNodes(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	var walk func([]taxonomy.Category)
	walk = func(categories []taxonomy.Category) {
		for _, category := range categories {
			for _, label := range []string{category.Name, category.Title} {
				for _, stem := range buyerNameTechnicalStems {
					if !strings.Contains(strings.ToLower(label), stem) {
						continue
					}
					exception := buyerNameTechnicalExceptions[category.ID]
					allowedLabel := label == category.Name || strings.HasSuffix(label, ": "+category.Name)
					if exception.reason == "" || exception.name != category.Name || !allowedLabel {
						t.Errorf("техническое имя без исключения: %s — %s", category.ID, label)
					}
					used[category.ID] = true
				}
			}
			walk(category.Children)
		}
	}
	walk(release.Categories)
	for id := range buyerNameTechnicalExceptions {
		if !used[id] {
			t.Errorf("неиспользуемое исключение имени: %s", id)
		}
	}
	for name, exceptions := range duplicateLeafNameExceptions {
		for id, reason := range exceptions {
			leaf, _, found := release.Definition().Category(id)
			if !found || normalizedName(leaf.Name) != name || reason == "" || len(leaf.Children) != 0 {
				t.Errorf("неверное исключение омонима: %s", id)
			}
		}
	}
}

func TestRequiredRetailBasicsRemainInExpandedAPI(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	definition := release.Definition()
	cases := map[string][]string{
		"shoes.men-boots":             {"size_footwear_label", "shoe_upper"},
		"shoes.women-pumps-kind":      {"size_footwear_label", "shoe_upper"},
		"sports.alpine-ski-boots":     {"size_footwear_label", "shoe_upper"},
		"clothing.women-down-jackets": {"size_clothing_label"},
		"clothing.girls-bras":         {"size_clothing_label"},
		"clothing.boys-suits":         {"size_clothing_label"},
		"clothing.women-thermal-tops": {"size_clothing_label"},
		"accessories.key-holders":     {"material"},
		"accessories.suitcases":       {"body_material", "length", "width", "height"},
		"appliances.kettles":          {"capacity_l"},
		"appliances.microwaves":       {"capacity_l"},
		"electronics.televisions":     {"screen_diagonal"},
	}
	for id, required := range cases {
		leaf, _, found := definition.Category(id)
		if !found {
			t.Fatal("нет проверенного листа", id)
		}
		actual := []string{}
		for _, field := range leaf.Attributes {
			if field.Required {
				actual = append(actual, field.Code)
			}
		}
		for _, code := range required {
			if !slices.Contains(actual, code) {
				t.Errorf("API потерял обязательность: %s.%s", id, code)
			}
		}
	}
}

func TestEveryClothingAndFootwearLeafRequiresMarkedSize(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	definition := release.Definition()
	var check func([]taxonomy.Category, string)
	check = func(categories []taxonomy.Category, code string) {
		for _, category := range categories {
			if len(category.Children) != 0 {
				check(category.Children, code)
				continue
			}
			leaf, _, found := definition.Category(category.ID)
			markedSize := code
			if category.Schema == "clothing_headwear" {
				// Профессиональный колпак маркируется размером головного убора, а не тела.
				markedSize = "headwear_size_label"
			}
			required := false
			for _, field := range leaf.Attributes {
				if field.Code == markedSize && field.Required {
					required = true
				}
			}
			if !found || !required {
				t.Errorf("лист потерял обязательную размерную маркировку: %s.%s", category.ID, markedSize)
			}
		}
	}
	for _, root := range release.Categories {
		if root.ID == "clothing" {
			check(root.Children, "size_clothing_label")
		}
		if root.ID == "shoes" {
			for _, family := range root.Children {
				if family.ID == "shoes.universal" {
					for _, common := range family.Children {
						if common.ID == "shoes.special" {
							check(common.Children, "size_footwear_label")
						}
					}
				} else {
					check(family.Children, "size_footwear_label")
				}
			}
		}
	}
}
