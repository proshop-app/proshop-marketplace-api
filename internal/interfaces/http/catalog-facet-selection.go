package http

import (
	"context"
	"net/url"
	"slices"
	"strings"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// Счётчики результата не раскрывают поисковый текст, значения фильтров или профили.
func rememberCatalogFacets(ctx context.Context, query ports.OfferQuery, facets []ports.Facet) {
	facts, _ := ctx.Value(keyFacts).(*requestFacts)
	if facts == nil {
		return
	}
	selector, class := catalogFacetSelector(query.Facets)
	counts := &catalogFacetFacts{
		selector: selector,
		class:    class,
		total:    len(facets),
	}
	for _, facet := range facets {
		if strings.HasPrefix(facet.Code, "attr.") {
			counts.attributes++
		}
	}
	facts.catalogFacets = counts
}

func catalogFacetSelector(facets *[]ports.FacetCode) (string, string) {
	if facets == nil {
		return "all", "all"
	}
	if len(*facets) == 0 {
		return "none", "none"
	}
	codes := make([]string, 0, len(*facets))
	for _, code := range *facets {
		codes = append(codes, string(code))
	}
	class := "partial"
	if len(codes) == 5 {
		class = "all"
	}
	return strings.Join(codes, ","), class
}

func parseOfferFacetSelection(values url.Values, query *ports.OfferQuery) error {
	raw, present := values["facets"]
	legacyEmpty := present && len(raw) == 1 && raw[0] == ""
	explicitNone := present && len(raw) == 1 && raw[0] == "none"
	if present {
		codes, err := parseFacetCodes(raw)
		if err != nil {
			return err
		}
		query.Facets = &codes
	}
	if raw, present := values["attributeFacets"]; present {
		if len(raw) != 1 || raw[0] != "true" && raw[0] != "false" {
			return appcatalog.ErrInvalidQuery.WithMessage("attributeFacets — true или false")
		}
		enabled := raw[0] == "true"
		if explicitNone && enabled || !legacyEmpty && !enabled && query.Facets != nil &&
			slices.Contains(*query.Facets, ports.FacetAttributes) {
			return appcatalog.ErrInvalidQuery.WithMessage("facets и attributeFacets противоречат друг другу")
		}
		// Старый флаг переводится здесь: приложение использует только единый список.
		switch {
		case query.Facets == nil && !enabled:
			codes := []ports.FacetCode{ports.FacetCategory, ports.FacetTenant, ports.FacetInStock, ports.FacetPrice}
			query.Facets = &codes
		case legacyEmpty && !enabled:
			codes := []ports.FacetCode{}
			query.Facets = &codes
		case query.Facets != nil && enabled && !slices.Contains(*query.Facets, ports.FacetAttributes):
			codes := append(*query.Facets, ports.FacetAttributes)
			query.Facets = &codes
		}
	}
	return nil
}

func parseFacetCodes(raw []string) ([]ports.FacetCode, error) {
	if len(raw) == 1 && raw[0] == "none" {
		return []ports.FacetCode{}, nil
	}
	if len(raw) == 1 && raw[0] == "" {
		// Пустой селектор PR44 уже опубликован как «только атрибуты».
		return []ports.FacetCode{ports.FacetAttributes}, nil
	}
	codes := []ports.FacetCode{}
	seen := map[ports.FacetCode]bool{}
	for _, list := range raw {
		for _, value := range strings.Split(list, ",") {
			code := ports.FacetCode(value)
			if !code.Valid() || seen[code] {
				return nil, appcatalog.ErrInvalidQuery.WithMessage("неизвестный или повторный фасет; none — отдельно")
			}
			codes = append(codes, code)
			seen[code] = true
		}
	}
	return codes, nil
}
