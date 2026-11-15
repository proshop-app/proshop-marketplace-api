package catalog

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// PopulateSellers обогащает готовую страницу одним чтением, не меняя состав и порядок карточек.
// Ошибку обрабатывает транспорт: счётчик не должен делать каталог недоступным.
func PopulateSellers(ctx context.Context, counter ports.SellersCounts, cards []OfferCard) error {
	keys := make([]string, 0, len(cards))
	seen := make(map[string]bool, len(cards))
	for i := range cards {
		cards[i].SellersCount = nil
		key := cards[i].Offer.ModelKey
		if key != "" && !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(keys) == 0 || counter == nil {
		return nil
	}
	counts, err := counter.CountSellers(ctx, keys)
	if err != nil {
		return err
	}
	for i := range cards {
		if key := cards[i].Offer.ModelKey; key != "" {
			count := counts[key]
			// Индекс и PostgreSQL могут расходиться: карточка уже видна,
			// поэтому ноль вводил бы покупателя в заблуждение.
			if count < 1 {
				continue
			}
			cards[i].SellersCount = &count
		}
	}
	return nil
}
