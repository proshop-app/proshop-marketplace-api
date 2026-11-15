package taxonomydata

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// auditRelease проверяет редакционные инварианты поставки поверх доменного контракта.
// Старые операторские снимки без пояснений остаются совместимыми с доменом.
func auditRelease(release taxonomy.Release) error {
	fields := release.Definition().Fields()
	descriptions := map[string]string{}
	for _, field := range release.Attributes {
		if err := auditAttribute(field); err != nil {
			return err
		}
		if !field.Deprecated {
			key := normalizedName(field.Description)
			if previous := descriptions[key]; previous != "" {
				return qualityError(field.Code, "повтор определения: "+previous)
			}
			descriptions[key] = field.Code
		}
	}
	for name, schema := range release.Schemas {
		names := map[string]string{}
		optional := map[string]bool{}
		for _, code := range schema.Optional {
			optional[code] = true
		}
		for _, code := range schema.Attributes {
			field := fields[code]
			key := normalizedName(field.Name)
			if previous := names[key]; previous != "" || field.Deprecated {
				return qualityError(name+"."+code, "повтор имени или исторический код в активной схеме")
			}
			names[key] = code
			formatted := field.Dictionary != "" || field.Type == "number" && field.Unit != ""
			if !optional[code] && !formatted && strings.TrimSpace(field.RequiredReason) == "" {
				return qualityError(name+"."+code, "нет формата или причины обязательного свободного поля")
			}
		}
	}
	if err := auditCategoryNames(release.Categories, 1); err != nil {
		return err
	}
	if err := auditLeafNames(release.Categories, map[string]string{}); err != nil {
		return err
	}
	return auditAudiencePlacement(release.Categories)
}

func auditAttribute(field taxonomy.Attribute) error {
	if strings.TrimSpace(field.Description) == "" || strings.TrimSpace(field.Example) == "" {
		return qualityError(field.Code, "нужны определение и пример")
	}
	if field.Type == "number" && field.Unit == "" || field.Type != "number" && field.Unit != "" {
		return qualityError(field.Code, "единица допустима и обязательна только у числа")
	}
	if field.Dictionary == "" && strings.TrimSpace(field.DictionaryNote) == "" {
		return qualityError(field.Code, "нужен словарь или причина его отсутствия")
	}
	if field.Dictionary != "" && field.Type != "string" && field.Type != "enum" {
		return qualityError(field.Code, "словарь не согласован с типом")
	}
	if field.Type == "string" && !field.Deprecated && field.Dictionary == "" && field.Facet {
		return qualityError(field.Code, "фильтруемая строка требует объявленного словаря")
	}
	if field.Multiple {
		return qualityError(field.Code, "карточка поддерживает только скаляр")
	}
	if field.Type == "number" {
		number, err := strconv.ParseFloat(field.Example, 64)
		if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			return qualityError(field.Code, "числовой пример должен быть числом без единицы")
		}
	}
	if field.Type == "boolean" && field.Example != "true" && field.Example != "false" {
		return qualityError(field.Code, "булев пример должен быть true или false")
	}
	if !field.Deprecated {
		switch normalizedName(field.Name) {
		case "тип", "вид", "назначение", "размер", "классзащиты":
			return qualityError(field.Code, "название без объекта или системы")
		}
	}
	return nil
}

func auditCategoryNames(categories []taxonomy.Category, depth int) error {
	if len(categories) > 0 && depth > 5 {
		return qualityError("categories", "редакционная глубина больше пяти")
	}
	names := map[string]bool{}
	axis := ""
	for _, category := range categories {
		if strings.Contains(normalizedName(category.Name), "унисекс") ||
			strings.Contains(category.ID, "unisex") {
			return qualityError(category.ID, "ветка унисекс запрещена; пол хранится атрибутом")
		}
		if len(category.Children) == 1 && singleChildAudienceExceptions[category.ID] == "" {
			return qualityError(category.ID, "обёртка с единственным ребёнком")
		}
		directLeaves := 0
		for _, child := range category.Children {
			if len(child.Children) == 0 {
				directLeaves++
			}
		}
		if directLeaves > 25 {
			return qualityError(category.ID, "больше 25 прямых листьев; нужны товарные подсемьи")
		}

		validAxis := category.Axis == "retail_department" || category.Axis == "product_family" ||
			category.Axis == "product_kind" || category.Axis == "audience" ||
			category.Axis == "publication_section" || category.Axis == "topic"
		if !validAxis || axis != "" && category.Axis != axis {
			return qualityError(category.ID, "нет единой оси деления соседей")
		}
		axis = category.Axis
		if category.Axis == "audience" && len(category.Children) == 0 {
			return qualityError(category.ID, "аудиторная область должна быть разделом, не листом")
		}
		if category.NameScope && category.Axis != "audience" {
			return qualityError(category.ID, "область имён допустима только у аудитории")
		}
		if len(category.Children) == 0 && strings.TrimSpace(category.Definition) == "" {
			return qualityError(category.ID, "нет границы вида изделия")
		}
		key := normalizedName(category.Name)
		if names[key] {
			return qualityError(category.ID, "одинаковые нормализованные имена соседей")
		}
		names[key] = true
		if err := auditCategoryNames(category.Children, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func normalizedName(value string) string {
	return strings.Map(
		func(char rune) rune {
			char = unicode.ToLower(char)
			if char == 'ё' {
				return 'е'
			}
			if !unicode.IsLetter(char) && !unicode.IsDigit(char) {
				return -1
			}
			return char
		},
		value,
	)
}

func qualityError(code, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidBundle, code, reason)
}

func auditLeafNames(categories []taxonomy.Category, seen map[string]string) error {
	for _, category := range categories {
		if len(category.Children) == 0 {
			key := normalizedName(category.Name)
			if previous := seen[key]; previous != "" {
				exceptions := duplicateLeafNameExceptions[key]
				if exceptions[previous] == "" || exceptions[category.ID] == "" {
					return qualityError(category.ID, "повтор имени листа: "+previous)
				}
			}
			seen[key] = category.ID
		}
		childrenSeen := seen
		if category.NameScope {
			// Повтор имени допускается между областями аудитории, внутри области — нет.
			childrenSeen = map[string]string{}
		}
		if err := auditLeafNames(category.Children, childrenSeen); err != nil {
			return err
		}
	}
	return nil
}

// Общий вид не может одновременно присутствовать внутри покупательской аудитории.
func auditAudiencePlacement(roots []taxonomy.Category) error {
	for _, root := range roots {
		common := map[string]string{}
		scoped := map[string]string{}
		var walk func([]taxonomy.Category, bool)
		walk = func(categories []taxonomy.Category, audience bool) {
			for _, category := range categories {
				inside := audience || category.NameScope
				if len(category.Children) == 0 {
					target := common
					if inside {
						target = scoped
					}
					target[normalizedName(category.Name)] = category.ID
				}
				walk(category.Children, inside)
			}
		}
		walk(root.Children, false)
		for name, id := range common {
			if duplicate := scoped[name]; duplicate != "" {
				return qualityError(id, "вид одновременно общий и аудиторный: "+duplicate)
			}
		}
	}
	return nil
}
