package taxonomy

import (
	"reflect"
	"slices"
)

// Revalidation хранит прогресс одной категории. Курсор фиксируется вместе с товарами и outbox.
type Revalidation struct {
	Version       int64
	Category      string
	TenantCursor  string
	ProductCursor string
	Processed     int64
	Done          bool
}

type categoryImpact struct {
	Category Category
	Path     []string
	Policy   string
}

// AffectedCategories сравнивает эффективные схемы, путь и наследуемую модерацию.
// Версия и правила открытых подсказок сами по себе не требуют обхода каталога.
func AffectedCategories(previous, next Definition) []string {
	before, after := categoryImpacts(previous.Categories), categoryImpacts(next.Categories)
	schemas := changedSchemas(previous, next)
	changed := []string{}
	for id, old := range before {
		current, exists := after[id]
		if !exists || !reflect.DeepEqual(old, current) || schemas[current.Category.Schema] {
			changed = append(changed, id)
		}
	}
	for id := range after {
		if _, exists := before[id]; !exists {
			changed = append(changed, id)
		}
	}
	slices.Sort(changed)
	return changed
}

func changedSchemas(previous, next Definition) map[string]bool {
	oldFields, newFields := previous.Fields(), next.Fields()
	changed := map[string]bool{}
	for name, schema := range next.Schemas {
		old, exists := previous.Schemas[name]
		changed[name] = !exists || !reflect.DeepEqual(old, schema)
		for _, code := range schema.Attributes {
			if !reflect.DeepEqual(oldFields[code], newFields[code]) {
				changed[name] = true
			}
		}
	}
	return changed
}

func categoryImpacts(categories []Category) map[string]categoryImpact {
	impacts := map[string]categoryImpact{}
	var walk func([]Category, []string, string)
	walk = func(nodes []Category, path []string, inherited string) {
		for _, node := range nodes {
			policy := inherited
			if node.Moderation != "" {
				policy = node.Moderation
			}
			currentPath := append(append([]string{}, path...), node.ID)
			children := node.Children
			node.Children = nil
			impacts[node.ID] = categoryImpact{
				Category: node,
				Path:     currentPath,
				Policy:   policy,
			}
			walk(children, currentPath, policy)
		}
	}
	walk(categories, nil, "auto")
	return impacts
}
