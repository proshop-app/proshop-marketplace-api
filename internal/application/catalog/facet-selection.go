package catalog

import "github.com/proshop-app/proshop-marketplace/internal/application/ports"

func validateFacetSelection(facets *[]ports.FacetCode) error {
	if facets == nil {
		return nil
	}
	seen := make(map[ports.FacetCode]bool, len(*facets))
	for _, code := range *facets {
		if !code.Valid() || seen[code] {
			return ErrInvalidQuery.WithMessage("неизвестный или повторный базовый фасет")
		}
		seen[code] = true
	}
	return nil
}
