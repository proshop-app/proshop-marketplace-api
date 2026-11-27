package catalog

import (
	"maps"
	"slices"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func (v *taxonomyDisplay) collectSearchFacetFields() map[string]taxonomy.Attribute {
	fields := make(map[string]taxonomy.Attribute)
	for _, panel := range v.panels {
		for _, filter := range panel.filters {
			if field, ok := v.fields[filter.Code]; ok {
				fields[filter.Code] = field
			}
		}
	}
	return fields
}

func (v *taxonomyDisplay) prepareFacetPanel(query *ports.OfferQuery, panel facetPanel) (facetDisplay, error) {
	automatic := query.Text != "" && query.CategoryID == "" && query.GroupID == ""
	if err := v.prepareFacetFilters(query, panel, automatic); err != nil {
		return facetDisplay{}, err
	}
	display := facetDisplay{
		taxonomy: v,
		sources:  panel.sources,
		order:    panel.apply(query, v.fields),
	}
	if !automatic {
		return display, validateListingFacetFields(*query)
	}

	requestsAttributes := false
	for _, key := range query.FacetKeys {
		if code, attribute := strings.CutPrefix(key, "attr."); attribute {
			requestsAttributes = true
			if _, known := v.searchFacetFields[code]; !known {
				return facetDisplay{}, ErrInvalidQuery.WithMessage("фасет отсутствует в панелях поиска")
			}
		}
	}
	if requestsAttributes || len(query.FacetKeys) == 0 && (query.Facets == nil || len(*query.Facets) != 0) {
		query.FacetTaxonomyVersion = v.definition.Version
	}
	if query.WantsFacet(ports.FacetAttributes) || len(query.FacetKeys) > 0 {
		display.retainSearchFacetFields(query)
	}
	return display, nil
}

func (v *taxonomyDisplay) prepareFacetFilters(query *ports.OfferQuery, panel facetPanel, automatic bool) error {
	if len(query.Attributes) == 0 {
		return nil
	}
	// Поисковый URL может содержать выбор из предыдущей панели. Разрешаем
	// ключи опубликованных панелей и сохраняем их при смене общего раздела.
	fields := v.searchFacetFields
	if !automatic {
		fields = make(map[string]taxonomy.Attribute, len(query.Attributes))
		for _, filter := range panel.filters {
			if _, selected := query.Attributes[filter.Code]; selected {
				fields[filter.Code] = v.fields[filter.Code]
			}
		}
	}
	if err := validateAttributeFilters(query.Attributes, fields); err != nil {
		return err
	}
	query.Attributes = maps.Clone(query.Attributes)
	for code, filter := range query.Attributes {
		filter.Type = fields[code].Type
		query.Attributes[code] = filter
	}
	return nil
}

func (v *taxonomyDisplay) resultFacetDisplay(query ports.OfferQuery, panel facetPanel) facetDisplay {
	display := facetDisplay{
		taxonomy: v,
		sources:  panel.sources,
		order:    panel.apply(&query, v.fields),
	}
	if query.WantsFacet(ports.FacetAttributes) || len(query.FacetKeys) > 0 {
		display.retainSearchFacetFields(&query)
	}
	return display
}

func (d *facetDisplay) retainSearchFacetFields(query *ports.OfferQuery) {
	codes := slices.Sorted(maps.Keys(query.Attributes))
	for _, key := range query.FacetKeys {
		if code, ok := strings.CutPrefix(key, "attr."); ok {
			codes = append(codes, code)
		}
	}
	for _, code := range codes {
		field, ok := d.taxonomy.searchFacetFields[code]
		if !ok || slices.ContainsFunc(query.FacetFields, func(existing taxonomy.Attribute) bool {
			return existing.Code == code
		}) {
			continue
		}
		if len(query.FacetFields) == MaxAttributeFacets && !d.makeRoomForSearchFacet(query) {
			continue
		}
		query.FacetFields = append(query.FacetFields, field)
		d.order = append(d.order, taxonomy.Filter{Code: code})
	}
	if len(query.FacetKeys) > 0 {
		query.FacetKeys = slices.DeleteFunc(slices.Clone(query.FacetKeys), func(key string) bool {
			code, attribute := strings.CutPrefix(key, "attr.")
			return attribute && !slices.ContainsFunc(query.FacetFields, func(field taxonomy.Attribute) bool {
				return field.Code == code
			})
		})
	}
}

func (d *facetDisplay) makeRoomForSearchFacet(query *ports.OfferQuery) bool {
	for i := len(query.FacetFields) - 1; i >= 0; i-- {
		code := query.FacetFields[i].Code
		if _, selected := query.Attributes[code]; selected || slices.Contains(query.FacetKeys, "attr."+code) {
			continue
		}
		query.FacetFields = slices.Delete(query.FacetFields, i, i+1)
		d.order = slices.DeleteFunc(d.order, func(filter taxonomy.Filter) bool {
			return filter.Code == code
		})
		return true
	}
	return false
}
