package catalog

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func displayFacets(facets []ports.Facet, query ports.OfferQuery, display facetDisplay) []ports.Facet {
	byCode := make(map[string]ports.Facet, len(facets))
	for _, facet := range facets {
		byCode[facet.Code] = facet
	}
	out := make([]ports.Facet, 0, len(display.order))
	for _, filter := range display.order {
		if code := wireBuiltin(filter.Code); code != "" {
			if facet, found := byCode[string(code)]; found {
				facet.Primary = filter.Primary
				out = append(out, facet)
			}
			continue
		}
		facet, found := byCode["attr."+filter.Code]
		if !found {
			continue
		}
		field := display.taxonomy.fields[filter.Code]
		facet = orderFacet(facet, field)
		if len(facet.Values) == 0 && facet.Min == nil && facet.Max == nil {
			if _, selected := query.Attributes[filter.Code]; !selected {
				continue
			}
		}
		facet.Primary = filter.Primary
		out = append(out, displayFacetValues(facet, field, display))
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

// Палитра задаёт порядок и допустимые образцы централизованно.
func orderFacet(facet ports.Facet, field taxonomy.Attribute) ports.Facet {
	if len(field.FacetOptions) == 0 {
		return facet
	}
	order := make(map[string]int, len(field.FacetOptions))
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
	return facet
}

// Представление значений берётся из того же снимка, по которому построен фасет.
func displayFacetValues(facet ports.Facet, field taxonomy.Attribute, display facetDisplay) ports.Facet {
	if field.Name != "" {
		facet.Name = field.BuyerName()
	}
	if len(field.ValuesMeta) == 0 && !field.DictionarySelection {
		return facet
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
	return facet
}
