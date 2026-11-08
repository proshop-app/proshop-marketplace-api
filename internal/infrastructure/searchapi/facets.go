package searchapi

import (
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func wireFacetSelection(facets *[]ports.FacetCode) *[]ports.FacetCode {
	if facets == nil {
		return nil
	}
	// Наличие селектора с нулём кодов означает [], даже если исходный slice nil.
	codes := make([]ports.FacetCode, len(*facets))
	copy(codes, *facets)
	return &codes
}

func wireFacetFields(fields []taxonomy.Attribute) []facetField {
	out := make([]facetField, 0, len(fields))

	for _, field := range fields {
		out = append(out, facetField{
			Code: field.Code,
			Name: field.Name,
			Type: field.Type,
			Unit: field.Unit,
		})
	}

	return out
}
