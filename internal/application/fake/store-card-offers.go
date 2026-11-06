package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (o offers) StoreCardOffers(
	_ context.Context,
	tenant ids.TenantID,
	after ids.ProductID,
	limit int,
) ([]ports.OfferState, error) {
	keys := o.keys(func(key productKey) bool {
		if key.tenant != tenant || key.product <= after {
			return false
		}
		p := o.d.products[key]
		marked := p.Card.Marketplace != nil && p.Card.Marketplace.Published
		return !p.Deleted && (marked || p.Card.Publication.Published)
	})
	return o.page(keys, limit), nil
}
