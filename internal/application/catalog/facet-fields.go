package catalog

import (
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// MaxAttributeFacets ограничивает стоимость агрегаций и размер панели покупателя.
// Это бюджет интерфейса, не размер глобального справочника; контракт поиска допускает 256.
const MaxAttributeFacets = 32

// scopeFields не ищет каждый лист заново от корня: на большой таксономии это квадратичная работа.
// Фильтры разрешены по объединению схем, фасеты — только по пересечению всех листьев области.
func scopeFields(
	d taxonomy.Definition,
	nodes []taxonomy.Category,
) (map[string]taxonomy.Attribute, map[string]taxonomy.Attribute) {
	fields := d.Fields()
	union := map[string]taxonomy.Attribute{}
	var common map[string]taxonomy.Attribute
	var collect func([]taxonomy.Category)
	collect = func(nodes []taxonomy.Category) {
		for _, node := range nodes {
			if len(node.Children) > 0 {
				collect(node.Children)
				continue
			}
			leaf := map[string]taxonomy.Attribute{}
			for _, code := range d.Schemas[node.Schema].Attributes {
				union[code] = fields[code]
				leaf[code] = fields[code]
			}
			if common == nil {
				common = leaf
				continue
			}
			for code := range common {
				if _, exists := leaf[code]; !exists {
					delete(common, code)
				}
			}
		}
	}
	collect(nodes)
	return union, common
}

func selectFacetFields(
	fields map[string]taxonomy.Attribute,
	filters map[string]ports.AttributeFilter,
) []taxonomy.Attribute {
	out := []taxonomy.Attribute{}
	for _, field := range fields {
		if field.Facet && !field.Deprecated {
			out = append(out, field)
		}
	}
	// Выбранные общие поля сохраняют управление фильтром при исчерпании бюджета.
	// Остальные упорядочены по коду, независимо от обхода map и дерева.
	sort.Slice(out, func(i, j int) bool {
		_, left := filters[out[i].Code]
		_, right := filters[out[j].Code]
		if left != right {
			return left
		}
		return out[i].Code < out[j].Code
	})
	if len(out) > MaxAttributeFacets {
		out = out[:MaxAttributeFacets]
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Code < out[j].Code
	})
	return out
}
