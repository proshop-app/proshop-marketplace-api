package catalog

import (
	"slices"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// MaxAttributeFacets — бюджет агрегаций по характеристикам в одном запросе.
const MaxAttributeFacets = taxonomy.MaxAttributeFilters

var builtinFacetOrder = [...]string{
	taxonomy.FilterCategory,
	taxonomy.FilterSeller,
	taxonomy.FilterPrice,
	taxonomy.FilterInStock,
}

type facetPanel struct {
	filters    []taxonomy.Filter
	sources    map[string][]string
	priorities bool
}

func (v *taxonomyDisplay) buildFacetPanels() map[string]facetPanel {
	panels := make(map[string]facetPanel)
	var visit func(taxonomy.Category) facetPanel
	visit = func(node taxonomy.Category) facetPanel {
		if len(node.Children) == 0 {
			panel := facetPanel{filters: v.definition.Schemas[node.Schema].Filters}
			for code, source := range v.sources[node.ID] {
				for _, filter := range panel.filters {
					if filter.Code == code {
						panel.addSource(code, source)
						break
					}
				}
			}
			panels[node.ID] = panel
			return panel
		}
		panel := facetPanel{filters: node.PriorityFilters, priorities: node.FacetLayout != nil}
		for _, child := range node.Children {
			for code, sources := range visit(child).sources {
				for _, source := range sources {
					panel.addSource(code, source)
				}
			}
		}
		panels[node.ID] = panel
		return panel
	}
	visit(taxonomy.Category{Children: v.definition.Categories, PriorityFilters: v.definition.RootFilters})
	return panels
}

func (p *facetPanel) addSource(code, source string) {
	if p.sources == nil {
		p.sources = map[string][]string{}
	}
	if !slices.Contains(p.sources[code], source) {
		p.sources[code] = append(p.sources[code], source)
	}
}

func (p facetPanel) apply(query *ports.OfferQuery, fields map[string]taxonomy.Attribute) []taxonomy.Filter {
	order := make([]taxonomy.Filter, 0, min(len(p.filters), MaxAttributeFacets)+len(builtinFacetOrder))
	query.FacetFields = []taxonomy.Attribute{}
	includeAttributes := query.WantsFacet(ports.FacetAttributes)
	var selection []ports.FacetCode
	if !p.priorities {
		for _, code := range builtinFacetOrder {
			if query.WantsFacet(wireBuiltin(code)) {
				order = append(order, taxonomy.Filter{Code: code, Primary: true})
			}
		}
	}
	for _, filter := range p.filters {
		if code := wireBuiltin(filter.Code); code != "" {
			if query.WantsFacet(code) {
				selection = append(selection, code)
				order = append(order, filter)
			}
			continue
		}
		if includeAttributes && len(query.FacetFields) < MaxAttributeFacets {
			query.FacetFields = append(query.FacetFields, fields[filter.Code])
			order = append(order, filter)
		}
	}
	if p.priorities {
		if includeAttributes {
			selection = append(selection, ports.FacetAttributes)
		}
		query.Facets = &selection
	}
	return order
}

func wireBuiltin(code string) ports.FacetCode {
	switch code {
	case taxonomy.FilterPrice:
		return ports.FacetPrice
	case taxonomy.FilterInStock:
		return ports.FacetInStock
	case taxonomy.FilterSeller:
		return ports.FacetTenant
	case taxonomy.FilterCategory:
		return ports.FacetCategory
	}
	return ""
}
