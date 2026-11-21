package catalog

import (
	"slices"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// MaxAttributeFacets ограничивает стоимость агрегаций и размер панели покупателя.
// Это бюджет интерфейса, не размер глобального справочника; контракт поиска допускает 256.
const MaxAttributeFacets = 32

// defaultPinnedFacetCode закрепляется, когда снимок таксономии не задаёт facetPinned.
const defaultPinnedFacetCode = "color"

// scopeFields не ищет каждый лист заново от корня: на большой таксономии это квадратичная работа.
// Фильтры разрешены по объединению схем, охват фасетов считается по листьям области.
func scopeFields(
	view *taxonomyDisplay,
	nodes []taxonomy.Category,
	collectUnion bool,
	collectSources bool,
) (map[string]taxonomy.Attribute, map[string]int, map[string][]string) {
	fields := view.fields
	union := map[string]taxonomy.Attribute{}
	coverage := map[string]int{}
	sources := map[string][]string{}
	var collect func([]taxonomy.Category)
	collect = func(nodes []taxonomy.Category) {
		for _, node := range nodes {
			if len(node.Children) > 0 {
				collect(node.Children)
				continue
			}
			schema := view.definition.Schemas[node.Schema]
			for _, code := range schema.Attributes {
				if collectUnion {
					union[code] = fields[code]
				}
				if !slices.Contains(schema.HiddenFacets, code) {
					coverage[code]++
					if collectSources {
						source := view.sources[node.ID][code]
						if source != "" && !slices.Contains(sources[code], source) {
							sources[code] = append(sources[code], source)
						}
					}
				}
			}
		}
	}
	collect(nodes)
	return union, coverage, sources
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
	// Закреплённые и выбранные поля сохраняют управление фильтром при исчерпании бюджета.
	// Остальные упорядочены по убыванию охвата, при равенстве — по коду.
	sort.Slice(out, func(i, j int) bool {
		leftPinned, rightPinned := isFacetPinned(out[i]), isFacetPinned(out[j])
		if leftPinned != rightPinned {
			return leftPinned
		}
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

func isFacetPinned(field taxonomy.Attribute) bool {
	if field.FacetPinned != nil {
		return *field.FacetPinned
	}
	return field.Code == defaultPinnedFacetCode
}
