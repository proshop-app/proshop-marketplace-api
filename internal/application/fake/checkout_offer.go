package fake

import (
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
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

// SeedBuyerOffer — предложение, которое видит покупатель: товар опубликован и допущен, магазин
// активен, участник подключён с доставкой. Уже заведённые участник и магазин не меняются.
func (s *Store) SeedBuyerOffer(id ids.OfferID, name string, price money.Money) {
	s.SeedReadyOffer(id, price)
	s.read(func(d *data) {
		key := productKey{id.Tenant, id.Product}
		product := d.products[key]
		product.Card.Name = name
		d.products[key] = product
		if _, ok := d.tenants[id.Tenant]; !ok {
			d.tenants[id.Tenant] = participant.TenantState{
				TenantID: id.Tenant,
				Slug:     string(id.Tenant),
				Name:     "Магазин",
				Status:   participant.TenantActive,
				Version:  1,
			}
		}
		if _, ok := d.participants[id.Tenant]; !ok {
			d.participants[id.Tenant] = participant.State{
				TenantID: id.Tenant,
				Slug:     string(id.Tenant),
				Name:     "Магазин",
				Stage:    participant.StageConnected,
				Enabled:  true,
				Delivery: participant.DeliverySettings{Regions: []string{"RU-MOW"}, Carriers: allCarriers()},
				Version:  1,
			}
		}
	})
}

// allCarriers — все службы доставки: настройки участника валидны, только если включены все.
func allCarriers() []string {
	carriers := []string{}
	for _, carrier := range delivery.Carriers() {
		carriers = append(carriers, string(carrier.Code))
	}
	return carriers
}
