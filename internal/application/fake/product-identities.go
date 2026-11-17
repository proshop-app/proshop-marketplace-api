package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (r products) SaveIdentities(_ context.Context, products []*catalog.Product) ([]ids.OfferID, error) {
	result := []ids.OfferID{}
	for _, product := range products {
		p := product.State()
		key := productKey{p.TenantID, p.ProductID}
		stored, ok := r.d.products[key]
		if !ok || stored.Version != product.OriginVersion() {
			continue
		}
		stored.Card.ModelKey = p.Card.ModelKey
		stored.Card.ModelKeySource = p.Card.ModelKeySource
		stored.Card.VariantKey = p.Card.VariantKey
		stored.Version = p.Version
		r.d.products[key] = stored
		product.MarkSaved()
		result = append(result, ids.OfferID{Tenant: p.TenantID, Product: p.ProductID})
	}
	return result, nil
}
