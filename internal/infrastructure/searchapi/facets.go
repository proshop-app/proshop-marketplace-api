package searchapi

import (
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func wireFacetSelection(facets *[]ports.FacetCode) *[]ports.FacetCode {
	if facets == nil {
		return nil
	}
	// Наличие селектора с нулём кодов означает [], даже если исходный slice nil.
	codes := make([]ports.FacetCode, 0, len(*facets))
	for _, code := range *facets {
		if code != ports.FacetAttributes {
			codes = append(codes, code)
		}
	}
	return &codes
}

func wireFacetFields(query ports.OfferQuery) []facetField {
	out := make([]facetField, 0, len(query.FacetFields))
	if !query.WantsFacet(ports.FacetAttributes) {
		return out
	}

	for _, field := range query.FacetFields {
		out = append(out, facetField{
			Code: field.Code,
			Name: field.Name,
			Type: field.Type,
			Unit: field.Unit,
		})
	}

	return out
}
