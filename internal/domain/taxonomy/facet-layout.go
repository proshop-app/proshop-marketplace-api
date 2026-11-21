package taxonomy

import (
	"maps"
	"slices"
)

const (
	// MaxAttributeFilters ограничивает число агрегаций по характеристикам выдачи.
	MaxAttributeFilters = 32
	maxPrimaryFilters   = 6

	// FacetOn включает фильтр в схеме и её наследниках.
	FacetOn FacetOverride = "on"
	// FacetOff выключает фильтр в схеме и её наследниках.
	FacetOff FacetOverride = "off"
	// FacetDefault возвращает глобальное правило характеристики.
	FacetDefault FacetOverride = "default"

	// FilterPrice — встроенный фильтр цены.
	FilterPrice = "@price"
	// FilterInStock — встроенный фильтр наличия.
	FilterInStock = "@inStock"
	// FilterSeller — встроенный фильтр продавца.
	FilterSeller = "@seller"
	// FilterCategory — встроенный фильтр подразделов.
	FilterCategory = "@category"
)

// FacetOverride — локальное правило фильтруемости характеристики.
type FacetOverride string

// FacetLayout задаёт порядок фильтров. Схема наследует весь объект, ветка задаёт свой.
type FacetLayout struct {
	Primary    []string `json:"primary" yaml:"primary"`
	Additional []string `json:"additional" yaml:"additional"`
}

// Filter — итоговое место характеристики или встроенного фильтра в панели.
type Filter struct {
	Code    string `json:"code"`
	Primary bool   `json:"primary"`
}

type facetScope struct {
	common map[string]bool
	union  map[string]bool
}

// CompileFacets рассчитывает панели и набор ключей индекса до публикации версии.
func (d Definition) CompileFacets(requireLayout bool) (Definition, error) {
	fields := d.Fields()
	schemas := make(map[string]Schema, len(d.Schemas))
	for name, schema := range d.Schemas {
		if err := schema.validateFacetOverrides(name, fields); err != nil {
			return Definition{}, err
		}
		filters, err := schema.compileFilters(name, fields)
		if err != nil {
			return Definition{}, err
		}
		schema.Filters = filters
		schemas[name] = schema
	}
	categories, scope, err := compileCategoryFilters(d.Categories, schemas, requireLayout)
	if err != nil {
		return Definition{}, err
	}
	d.Schemas = schemas
	d.Categories = categories
	d.FacetKeys = slices.AppendSeq(make([]string, 0, len(scope.union)), maps.Keys(scope.union))
	slices.Sort(d.FacetKeys)
	d.RootFilters = defaultFilters(scope.common)
	return d, nil
}

func (s Schema) validateFacetOverrides(name string, fields map[string]Attribute) error {
	for code, mode := range s.FacetOverrides {
		if !slices.Contains(s.Attributes, code) || (mode == FacetOn && fields[code].Deprecated) {
			return ErrInvalid.WithMessage("неверный код facetOverrides: " + name + "." + code)
		}
		switch mode {
		case FacetOn, FacetOff, FacetDefault:
		default:
			return ErrInvalid.WithMessage("неверное значение facetOverrides: " + name + "." + code)
		}
	}
	return nil
}

func (s Schema) compileFilters(name string, fields map[string]Attribute) ([]Filter, error) {
	allowed := make(map[string]bool, len(s.Attributes))
	for _, code := range s.Attributes {
		field := fields[code]
		mode := s.FacetOverrides[code]
		if !field.Deprecated && (mode == FacetOn || (mode != FacetOff && field.Facet)) {
			allowed[code] = true
		}
	}
	layout := s.FacetLayout
	if layout == nil {
		return defaultFilters(allowed), nil
	}
	if layout.Primary == nil || layout.Additional == nil || len(layout.Primary) > maxPrimaryFilters ||
		len(layout.Primary)+len(layout.Additional) != len(allowed) || len(allowed) > MaxAttributeFilters {
		return nil, ErrInvalid.WithMessage("неполный или слишком большой facetLayout схемы: " + name)
	}
	return layout.resolve(name, allowed, false)
}

func compileCategoryFilters(
	nodes []Category,
	schemas map[string]Schema,
	requireLayout bool,
) ([]Category, facetScope, error) {
	if len(nodes) == 0 {
		return nil, facetScope{}, nil
	}
	categories := slices.Clone(nodes)
	scope := facetScope{union: map[string]bool{}}
	for i := range categories {
		node := &categories[i]
		child, err := node.compileFilters(schemas, requireLayout)
		if err != nil {
			return nil, facetScope{}, err
		}
		scope.include(child)
	}
	return categories, scope, nil
}

func (node *Category) compileFilters(schemas map[string]Schema, requireLayout bool) (facetScope, error) {
	if len(node.Children) == 0 {
		if node.FacetLayout != nil {
			return facetScope{}, ErrInvalid.WithMessage("facetLayout листа задаётся в схеме: " + node.ID)
		}
		node.PriorityFilters = nil
		schema := schemas[node.Schema]
		if requireLayout && schema.FacetLayout == nil {
			return facetScope{}, ErrInvalid.WithMessage("отсутствует facetLayout схемы: " + node.Schema)
		}
		filters := schema.Filters
		keys := make(map[string]bool, len(filters))
		for _, filter := range filters {
			keys[filter.Code] = true
		}
		return facetScope{common: keys, union: keys}, nil
	}
	children, scope, err := compileCategoryFilters(node.Children, schemas, requireLayout)
	if err != nil {
		return facetScope{}, err
	}
	node.Children = children
	layout := node.FacetLayout
	if layout == nil {
		if requireLayout && len(scope.union) > MaxAttributeFilters {
			return facetScope{}, ErrInvalid.WithMessage("отсутствует facetLayout широкой ветки: " + node.ID)
		}
		node.PriorityFilters = defaultFilters(scope.common)
		return scope, nil
	}
	if len(layout.Primary) < 5 || len(layout.Primary) > 8 || layout.Additional == nil || len(layout.Additional) != 0 {
		return facetScope{}, ErrInvalid.WithMessage("facetLayout ветки требует от 5 до 8 приоритетов: " + node.ID)
	}
	node.PriorityFilters, err = layout.resolve(node.ID, scope.common, true)
	return scope, err
}

func (s *facetScope) include(child facetScope) {
	maps.Copy(s.union, child.union)
	if s.common == nil {
		s.common = maps.Clone(child.common)
		return
	}
	for code := range s.common {
		if !child.common[code] {
			delete(s.common, code)
		}
	}
}

func (l FacetLayout) resolve(name string, allowed map[string]bool, branch bool) ([]Filter, error) {
	out := make([]Filter, 0, len(l.Primary)+len(l.Additional))
	seen := make(map[string]bool, cap(out))
	for i, group := range [][]string{l.Primary, l.Additional} {
		for _, code := range group {
			if seen[code] || !allowed[code] && (!branch || !builtinFilter(code)) {
				return nil, ErrInvalid.WithMessage("неверный код facetLayout: " + name + "." + code)
			}
			seen[code] = true
			out = append(out, Filter{Code: code, Primary: i == 0})
		}
	}
	return out, nil
}

func builtinFilter(code string) bool {
	switch code {
	case FilterPrice, FilterInStock, FilterSeller, FilterCategory:
		return true
	}
	return false
}

func defaultFilters(allowed map[string]bool) []Filter {
	keys := slices.Sorted(maps.Keys(allowed))
	out := make([]Filter, 0, len(keys))
	for i, code := range keys {
		out = append(out, Filter{Code: code, Primary: i < maxPrimaryFilters})
	}
	return out
}
