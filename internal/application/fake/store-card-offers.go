package fake

import (
	"context"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
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

func (o offers) WalkStoreSummaryOffers(
	_ context.Context,
	tenant ids.TenantID,
	visit func([]ports.OfferState) error,
) error {
	keys := o.keys(func(key productKey) bool {
		p := o.d.products[key]
		marked := p.Card.Marketplace != nil && p.Card.Marketplace.Published
		return key.tenant == tenant && !p.Deleted && (marked || p.Card.Publication.Published)
	})
	sort.Slice(keys, func(i, j int) bool {
		a, b := catalog.RatingGroup(o.d.products[keys[i]]), catalog.RatingGroup(o.d.products[keys[j]])
		if a != b {
			return a < b
		}
		return keys[i].product < keys[j].product
	})
	for start := 0; start < len(keys); start += 200 {
		states := o.page(keys[start:min(start+200, len(keys))], 200)
		if err := visit(states); err != nil {
			return err
		}
	}
	return nil
}
