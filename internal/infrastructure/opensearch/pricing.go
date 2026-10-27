package opensearch

import (
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type priceProfileDocument struct {
	Context        string `json:"context"`
	Key            string `json:"key"`
	Price          int64  `json:"price"`
	EffectivePrice int64  `json:"effectivePrice"`
}

func priceProfileDocuments(doc ports.OfferDocument) []priceProfileDocument {
	out := make([]priceProfileDocument, 0, len(doc.PriceProfiles))
	for _, profile := range doc.PriceProfiles {
		out = append(out, priceProfileDocument{
			Context:        doc.TenantID + ":" + profile.Key,
			Key:            profile.Key,
			Price:          profile.Price.Kopecks(),
			EffectivePrice: profile.EffectivePrice.Kopecks(),
		})
	}
	return out
}

func profileFilter(query ports.OfferQuery) map[string]any {
	if query.PriceContexts == nil {
		return term("priceProfiles.key", catalog.DefaultPriceProfile)
	}
	contexts := make([]string, 0, len(query.PriceContexts))
	for tenant, key := range query.PriceContexts {
		contexts = append(contexts, string(tenant)+":"+key)
	}
	return map[string]any{"terms": map[string]any{"priceProfiles.context": contexts}}
}

func profileQuery(query ports.OfferQuery) map[string]any {
	filters := []any{profileFilter(query)}
	if query.PriceMin != nil || query.PriceMax != nil {
		bounds := map[string]any{}
		if query.PriceMin != nil {
			bounds["gte"] = *query.PriceMin
		}
		if query.PriceMax != nil {
			bounds["lte"] = *query.PriceMax
		}
		filters = append(filters, map[string]any{"range": map[string]any{"priceProfiles.effectivePrice": bounds}})
	}
	return map[string]any{"nested": map[string]any{
		"path":  "priceProfiles",
		"query": map[string]any{"bool": map[string]any{"filter": filters}},
	}}
}

func profileSort(query ports.OfferQuery, order string) map[string]any {
	return map[string]any{"priceProfiles.effectivePrice": map[string]any{
		"order": order,
		"nested": map[string]any{
			"path":   "priceProfiles",
			"filter": profileFilter(query),
		},
	}}
}

func selectProfile(doc ports.OfferDocument, query ports.OfferQuery) (ports.OfferDocument, bool) {
	key := catalog.DefaultPriceProfile
	if query.PriceContexts != nil {
		key = query.PriceContexts[ids.TenantID(doc.TenantID)]
	}
	profile, found := catalog.SelectPrice(doc.PriceProfiles, key)
	if !found {
		return ports.OfferDocument{}, false
	}
	doc.Price = profile.EffectivePrice.Kopecks()
	doc.BasePrice = profile.Price.Kopecks()
	return doc, true
}
