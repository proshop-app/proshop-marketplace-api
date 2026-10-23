package catalog

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// FacetCoveragePercent — каждый пятый результат: убирает редкие поля, сохраняя полезные в смешанной выдаче.
const FacetCoveragePercent = 20

func visibleFacets(facets []ports.Facet, total int) []ports.Facet {
	out := []ports.Facet{}
	for _, f := range facets {
		if !strings.HasPrefix(f.Code, "attr.") || (total > 0 && f.Count*100 >= total*FacetCoveragePercent) {
			out = append(out, f)
		}
	}
	return out
}

func validateAttributeFilters(filters map[string]ports.AttributeFilter, fields map[string]taxonomy.Attribute) error {
	if len(filters) > taxonomy.MaxAttributes {
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
				for _, allowed := range a.Values {
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
