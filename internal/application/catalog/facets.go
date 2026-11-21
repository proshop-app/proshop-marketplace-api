package catalog

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// FacetCoveragePercent — каждый пятый результат: убирает редкие поля, сохраняя полезные в смешанной выдаче.
const FacetCoveragePercent = 20

func displayFacets(
	facets []ports.Facet,
	total int,
	query ports.OfferQuery,
	display facetDisplay,
) []ports.Facet {
	// При total=0 снятие собственного фильтра ещё может вернуть варианты.
	out := []ports.Facet{}
	for _, f := range facets {
		code := ports.FacetCode(f.Code)
		if strings.HasPrefix(f.Code, "attr.") {
			code = ports.FacetAttributes
		}
		if !query.WantsFacet(code) {
			continue
		}
		f = orderFacet(f, query.FacetFields)
		f = displayFacetValues(f, query.FacetFields, display)
		if visibleAttributeFacet(f, total, query) {
			out = append(out, f)
		}
	}
	return out
}

func validateAttributeFilters(filters map[string]ports.AttributeFilter, fields map[string]taxonomy.Attribute) error {
	if len(filters) > ports.MaxSearchAttributes {
		return ErrInvalidQuery
	}
	for code, filter := range filters {
		a, ok := fields[code]
		if !ok || !taxonomy.ValidCode(code) || len(filter.Values) > 100 {
			return ErrInvalidQuery
		}
		if filter.Min != "" || filter.Max != "" {
			if a.Type != "number" || len(filter.Values) > 0 {
				return ErrInvalidQuery
			}
			var min, max float64
			var err error
			if filter.Min != "" {
				min, err = attributeNumber(filter.Min)
				if err != nil {
					return err
				}
			}
			if filter.Max != "" {
				max, err = attributeNumber(filter.Max)
				if err != nil {
					return err
				}
			}
			if filter.Min != "" && filter.Max != "" && min > max {
				return ErrInvalidQuery
			}
		}
		for _, value := range filter.Values {
			if value == "" || utf8.RuneCountInString(value) > 1024 {
				return ErrInvalidQuery
			}
			switch a.Type {
			case "number":
				if _, err := attributeNumber(value); err != nil {
					return err
				}
			case "boolean":
				if value != "true" && value != "false" {
					return ErrInvalidQuery
				}
			case "enum":
				found := false
				options := a.FacetOptions
				if options == nil {
					options = a.Values
				}
				for _, allowed := range options {
					found = found || value == allowed
				}
				if !found {
					return ErrInvalidQuery
				}
			}
		}
	}
	return nil
}

func attributeNumber(raw string) (float64, error) {
	if !json.Valid([]byte(raw)) || raw == "null" {
		return 0, ErrInvalidQuery
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, ErrInvalidQuery
	}
	return value, nil
}

// Палитра задаёт порядок образцов; явно закреплённые фасеты без палитры сортируются по числу товаров.
func orderFacet(facet ports.Facet, fields []taxonomy.Attribute) ports.Facet {
	for _, field := range fields {
		if facet.Code != "attr."+field.Code {
			continue
		}
		if len(field.FacetOptions) == 0 && field.FacetPinned != nil && *field.FacetPinned {
			sort.SliceStable(facet.Values, func(i, j int) bool {
				if facet.Values[i].Count != facet.Values[j].Count {
					return facet.Values[i].Count > facet.Values[j].Count
				}
				return facet.Values[i].Value < facet.Values[j].Value
			})
			return facet
		}
		if len(field.FacetOptions) == 0 {
			break
		}
		order := map[string]int{}
		for i, value := range field.FacetOptions {
			order[value] = i
		}
		values := []ports.FacetBucket{}
		for _, bucket := range facet.Values {
			if _, ok := order[bucket.Value]; ok {
				values = append(values, bucket)
			}
		}
		sort.Slice(values, func(i, j int) bool { return order[values[i].Value] < order[values[j].Value] })
		facet.Values = values
		break
	}
	return facet
}

// Представление значений берётся из того же снимка, по которому построен фасет.
func displayFacetValues(facet ports.Facet, fields []taxonomy.Attribute, display facetDisplay) ports.Facet {
	for _, field := range fields {
		if facet.Code != "attr."+field.Code {
			continue
		}
		if field.Name != "" {
			facet.Name = field.BuyerName()
		}
		if len(field.ValuesMeta) == 0 && !field.DictionarySelection {
			break
		}
		values := append([]ports.FacetBucket(nil), facet.Values...)
		for i := range values {
			values[i].Meta = display.meta(field, values[i].Value).DisplayMeta()
		}
		if field.DictionarySelection {
			sortAttributeValues(
				values,
				func(bucket ports.FacetBucket) string { return bucket.Value },
				func(bucket ports.FacetBucket) *int {
					if bucket.Meta == nil {
						return nil
					}
					return bucket.Meta.Order
				},
			)
		}
		if len(values) > 0 {
			facet.Values = values
		}
		break
	}
	return facet
}

// Выбор сохраняет управление закреплённым фасетом даже без альтернатив.
func visibleAttributeFacet(facet ports.Facet, total int, query ports.OfferQuery) bool {
	if !strings.HasPrefix(facet.Code, "attr.") {
		return true
	}
	for _, field := range query.FacetFields {
		if facet.Code != "attr."+field.Code {
			continue
		}
		if isFacetPinned(field) {
			if len(query.Attributes[field.Code].Values) > 0 {
				return true
			}
			if len(facet.Values) < 2 {
				return false
			}
			return facet.Count > 0
		}
	}
	return facet.Count > 0 && facet.Count*100 >= total*FacetCoveragePercent
}
