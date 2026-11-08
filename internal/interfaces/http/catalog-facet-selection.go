package http

import (
	"context"
	"net/url"
	"strings"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// Счётчики результата позволяют измерить долю выдач без фасетов по журналу,
// не записывая поисковый текст, значения фильтров или персональные профили.
func rememberCatalogFacets(ctx context.Context, facets []ports.Facet) {
	facts, _ := ctx.Value(keyFacts).(*requestFacts)
	if facts == nil {
		return
	}
	counts := &catalogFacetFacts{
		total: len(facets),
	}
	for _, facet := range facets {
		if strings.HasPrefix(facet.Code, "attr.") {
			counts.attributes++
		}
	}
	facts.catalogFacets = counts
}

func parseOfferFacetSelection(values url.Values, query *ports.OfferQuery) error {
	if raw, present := values["facets"]; present {
		if len(raw) != 1 {
			return appcatalog.ErrInvalidQuery.WithMessage("facets передаётся одним списком через запятую")
		}
		codes := []ports.FacetCode{}
		seen := map[ports.FacetCode]bool{}
		if raw[0] != "" {
			for _, value := range strings.Split(raw[0], ",") {
				code := ports.FacetCode(value)
				if !code.Valid() || seen[code] {
					return appcatalog.ErrInvalidQuery.WithMessage("неизвестный или повторный базовый фасет")
				}
				codes = append(codes, code)
				seen[code] = true
			}
		}
		query.Facets = &codes
	}
	if raw, present := values["attributeFacets"]; present {
		if len(raw) != 1 || raw[0] != "true" && raw[0] != "false" {
			return appcatalog.ErrInvalidQuery.WithMessage("attributeFacets — true или false")
		}
		enabled := raw[0] == "true"
		query.AttributeFacets = &enabled
	}
	return nil
}
