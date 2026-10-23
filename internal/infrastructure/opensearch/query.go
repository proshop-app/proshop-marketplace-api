package opensearch

import "github.com/proshop-app/proshop-marketplace/internal/application/ports"

// searchBody — запрос выдачи: только предложения в выдаче, фильтры без
// влияния на релевантность, детерминированный порядок при равенстве.
func searchBody(query ports.OfferQuery) map[string]any {
	filters := []any{term("listed", true)}

	if query.TenantID != "" {
		filters = append(filters, term("tenantId", query.TenantID))
	}
	if query.CategoryID != "" {
		filters = append(filters, term("categories.id", query.CategoryID))
	}
	if query.Tag != "" {
		filters = append(filters, term("tags", query.Tag))
	}
	if query.InStock {
		filters = append(filters, term("inStock", true))
	}
	if query.PriceMin != nil || query.PriceMax != nil {
		bounds := map[string]any{}
		if query.PriceMin != nil {
			bounds["gte"] = *query.PriceMin
		}
		if query.PriceMax != nil {
			bounds["lte"] = *query.PriceMax
		}

		filters = append(filters, map[string]any{"range": map[string]any{"price": bounds}})
	}

	boolQuery := map[string]any{"filter": filters}
	if query.Text != "" {
		boolQuery["must"] = []any{map[string]any{
			"multi_match": map[string]any{
				"query":    query.Text,
				"fields":   []string{"name^4", "sku^3", "sellerName^2", "categories.name^2", "description"},
				"type":     "best_fields",
				"operator": "and",
			},
		}}
	}

	return map[string]any{
		"query":            map[string]any{"bool": boolQuery},
		"from":             query.Offset,
		"size":             query.Limit,
		"track_total_hits": true,
		"version":          true,
		"sort":             sortOf(query),
	}
}

func term(field string, value any) map[string]any {
	return map[string]any{"term": map[string]any{field: value}}
}

func sortOf(query ports.OfferQuery) []any {
	tie := map[string]any{"offerId": "asc"}

	switch query.Sort {
	case ports.SortPriceAsc:
		return []any{map[string]any{"price": "asc"}, tie}
	case ports.SortPriceDesc:
		return []any{map[string]any{"price": "desc"}, tie}
	case ports.SortNewest:
		return []any{map[string]any{"updatedAt": "desc"}, tie}
	default:
		if query.Text == "" {
			return []any{map[string]any{"updatedAt": "desc"}, tie}
		}

		return []any{"_score", tie}
	}
}
