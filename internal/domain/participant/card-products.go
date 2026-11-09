package participant

import (
	"reflect"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CardProducts — закрепления магазина; свободные места заполняются популярными товарами.
type CardProducts struct {
	Enabled    bool     `json:"enabled"`
	ProductIDs []string `json:"productIds"`
}

// DefaultCardProducts включает автоматический подбор без закреплений.
func DefaultCardProducts() CardProducts {
	return CardProducts{Enabled: true, ProductIDs: []string{}}
}

// Normalize отделяет список от вызывающего кода и проверяет контракт закреплений.
func (p CardProducts) Normalize() (CardProducts, error) {
	if !p.Enabled {
		return CardProducts{Enabled: false, ProductIDs: []string{}}, nil
	}
	if len(p.ProductIDs) > 3 {
		return CardProducts{}, ErrInvalidCardProducts
	}
	used := map[string]bool{}
	for _, id := range p.ProductIDs {
		if _, err := ids.ParseProductID(id); err != nil || used[id] {
			return CardProducts{}, ErrInvalidCardProducts
		}
		used[id] = true
	}
	p.ProductIDs = append([]string{}, p.ProductIDs...)
	return p, nil
}

// ProductSettings поддерживает старые снимки без новой настройки.
func (c StoreCard) ProductSettings() CardProducts {
	if c.Products == nil {
		return DefaultCardProducts()
	}
	products, err := c.Products.Normalize()
	if err != nil {
		return DefaultCardProducts()
	}
	return products
}

// ApplyCardProducts меняет только настройку товаров, не готовность карточки.
func (p *Participant) ApplyCardProducts(products CardProducts, now time.Time) (bool, error) {
	next, err := products.Normalize()
	if err != nil {
		return false, err
	}
	if reflect.DeepEqual(p.state.StoreCard.Products, &next) {
		return false, nil
	}
	p.state.StoreCard.Products = &next
	p.touch(now)
	return true, nil
}
