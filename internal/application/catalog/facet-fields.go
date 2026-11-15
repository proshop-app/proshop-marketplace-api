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
// Фильтры разрешены по объединению схем, охват фасетов считается по листьям области.
func scopeFields(
	d taxonomy.Definition,
	nodes []taxonomy.Category,
	collectUnion bool,
) (map[string]taxonomy.Attribute, map[string]int) {
	fields := d.Fields()
	union := map[string]taxonomy.Attribute{}
	coverage := map[string]int{}
	var collect func([]taxonomy.Category)
	collect = func(nodes []taxonomy.Category) {
		for _, node := range nodes {
			if len(node.Children) > 0 {
				collect(node.Children)
				continue
			}
			for _, code := range d.Schemas[node.Schema].Attributes {
				if collectUnion {
					union[code] = fields[code]
				}
				coverage[code]++
			}
		}
	}
	collect(nodes)
	return union, coverage
}

func selectFacetFields(
	fields map[string]taxonomy.Attribute,
	coverage map[string]int,
	filters map[string]ports.AttributeFilter,
) []taxonomy.Attribute {
	out := []taxonomy.Attribute{}
	for code := range coverage {
		field := fields[code]
		if field.Facet && !field.Deprecated {
			out = append(out, field)
		}
	}
	// Выбранные поля сохраняют управление фильтром при исчерпании бюджета.
	// Остальные упорядочены по убыванию охвата, при равенстве — по коду.
	sort.Slice(out, func(i, j int) bool {
		_, left := filters[out[i].Code]
		_, right := filters[out[j].Code]
		if left != right {
			return left
		}
		if coverage[out[i].Code] != coverage[out[j].Code] {
			return coverage[out[i].Code] > coverage[out[j].Code]
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
