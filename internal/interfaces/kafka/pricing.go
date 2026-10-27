package kafka

import (
	"encoding/json"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
)

type priceProfileJSON struct {
	Key            string      `json:"key"`
	Price          json.Number `json:"price"`
	EffectivePrice json.Number `json:"effectivePrice"`
}

func snapshotPrices(values []priceProfileJSON) ([]catalog.PriceProfile, error) {
	profiles := make([]catalog.PriceProfile, 0, len(values))

	for _, value := range values {
		if !strings.HasPrefix(value.Key, "marketplace:") {
			continue
		}
		price, err := rubles(value.Price)
		if err != nil {
			return nil, err
		}
		effective, err := rubles(value.EffectivePrice)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, catalog.PriceProfile{
			Key:            value.Key,
			Price:          price,
			EffectivePrice: effective,
		})
	}

	return profiles, catalog.ValidatePrices(profiles)
}
