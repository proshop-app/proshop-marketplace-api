package catalog

import (
	"strings"

	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// DefaultPriceProfile — публичная цена канала без группы покупателя.
const DefaultPriceProfile = "marketplace:default"

// PriceProfile — восстанавливаемая цена канала или группы; не публичный прайс.
type PriceProfile struct {
	Key            string      `json:"key"`
	Price          money.Money `json:"price"`
	EffectivePrice money.Money `json:"effectivePrice"`
}

// SelectPrice выбирает ровно один профиль, включая нулевую цену.
func SelectPrice(profiles []PriceProfile, key string) (PriceProfile, bool) {
	for _, profile := range profiles {
		if profile.Key == key {
			return profile, true
		}
	}

	return PriceProfile{}, false
}

// ValidatePrices отклоняет дубли и недопустимые суммы до хранения проекции.
func ValidatePrices(profiles []PriceProfile) error {
	seen := map[string]bool{}

	for _, profile := range profiles {
		if !strings.HasPrefix(profile.Key, "marketplace:") || seen[profile.Key] ||
			profile.Price.IsNegative() || profile.EffectivePrice.IsNegative() {
			return ErrInvalidSnapshot.WithMessage("некорректный ценовой профиль")
		}
		seen[profile.Key] = true
	}

	return nil
}
