package catalog

import (
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

const maxListingFacetKeys = 50

func validateListingFacetKeys(keys []string) error {
	if len(keys) > maxListingFacetKeys {
		return ErrInvalidQuery.WithMessage("слишком много запрошенных фасетов")
	}
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if seen[key] {
			return ErrInvalidQuery.WithMessage("фасеты не должны повторяться")
		}
		switch key {
		case "category", "tenant", "inStock", "price":
		default:
			code, attribute := strings.CutPrefix(key, "attr.")
			if !attribute || !taxonomy.ValidCode(code) {
				return ErrInvalidQuery.WithMessage("некорректный ключ фасета")
			}
		}
		seen[key] = true
	}
	return nil
}

func listingFacetKeys(query *ports.OfferQuery) map[string]bool {
	keys := make(map[string]bool, len(query.FacetKeys)+1)
	for _, key := range query.FacetKeys {
		keys[key] = true
	}
	if len(query.FacetKeys) > 0 {
		for code := range query.Attributes {
			keys["attr."+code] = true
		}
		keys["category"] = keys["category"] || query.CategoryID != ""
		keys["tenant"] = keys["tenant"] || len(query.TenantIDs) > 0
		keys["inStock"] = keys["inStock"] || query.InStockSet || query.InStock
		keys["price"] = keys["price"] || query.PriceMin != nil || query.PriceMax != nil
	}
	if query.PriceHistogram {
		keys["price"] = true
	}
	return keys
}

func validateListingFacetFields(query ports.OfferQuery) error {
	for _, key := range query.FacetKeys {
		code, attribute := strings.CutPrefix(key, "attr.")
		if !attribute {
			continue
		}
		available := false
		for _, field := range query.FacetFields {
			if field.Code == code {
				available = true
				break
			}
		}
		if !available {
			return ErrInvalidQuery.WithMessage("фасет отсутствует в панели категории")
		}
	}
	return nil
}
