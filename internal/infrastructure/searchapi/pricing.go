package searchapi

import (
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func encodePrices(profiles []catalog.PriceProfile) []priceProfile {
	out := make([]priceProfile, 0, len(profiles))

	for _, profile := range profiles {
		out = append(out, priceProfile{
			Key:            profile.Key,
			Price:          profile.Price.Kopecks(),
			EffectivePrice: profile.EffectivePrice.Kopecks(),
		})
	}

	return out
}

func wirePriceContexts(contexts map[ids.TenantID]string) map[string]string {
	if contexts == nil {
		return nil
	}

	out := make(map[string]string, len(contexts))

	for tenant, key := range contexts {
		out[string(tenant)] = key
	}

	return out
}
