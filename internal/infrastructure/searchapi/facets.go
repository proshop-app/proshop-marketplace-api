package searchapi

import "github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"

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
