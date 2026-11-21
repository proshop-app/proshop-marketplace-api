package kafka

import (
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
)

const marketplaceDefaultPriceProfile = "marketplace:default"

type priceProfileJSON struct {
	Key            string      `json:"key"`
	Price          json.Number `json:"price"`
	EffectivePrice json.Number `json:"effectivePrice"`
}

// snapshotPrices берёт только публичную цену канала; остальные профили не хранятся.
func snapshotPrices(snapshot *catalog.Snapshot, values []priceProfileJSON) error {
	for _, value := range values {
		if value.Key != marketplaceDefaultPriceProfile {
			continue
		}
		if snapshot.HasPrice {
			return ErrInvalidPayload.WithMessage("повтор публичной цены")
		}
		base, err := rubles(value.Price)
		if err != nil {
			return err
		}
		price, err := rubles(value.EffectivePrice)
		if err != nil {
			return err
		}
		if base.IsNegative() || price.IsNegative() {
			return ErrInvalidPayload.WithMessage("отрицательная публичная цена")
		}
		snapshot.HasPrice = true
		snapshot.Price = price
		snapshot.BasePrice = base
	}
	return nil
}
