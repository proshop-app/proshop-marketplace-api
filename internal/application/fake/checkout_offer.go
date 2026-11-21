package fake

import (
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// SeedReadyOffer — согласованная проекция для тестов оформления, независимо от поискового индекса.
func (s *Store) SeedReadyOffer(id ids.OfferID, price money.Money) {
	s.SeedProduct(catalog.ProductState{
		TenantID:  id.Tenant,
		ProductID: id.Product,
		Card:      catalog.Card{Visible: true, Publication: taxonomy.Publication{Published: true}},
		HasPrice:  true,
		Price:     price,
		BasePrice: price,
	})
	s.SeedVerdict(moderation.VerdictState{TenantID: id.Tenant, ProductID: id.Product, Status: moderation.Allowed})
}
