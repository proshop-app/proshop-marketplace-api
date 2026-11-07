package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// SetPromoPricing — задаёт результат проверки промокода для сценария.
func (p *Platform) SetPromoPricing(tenant ids.TenantID, result ports.GroupPricing) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.promos == nil {
		p.promos = make(map[ids.TenantID]ports.GroupPricing)
	}
	p.promos[tenant] = result
}

// PriceGroup — проверяет код и строки одной группы магазина.
func (p *Platform) PriceGroup(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.GroupPricingRequest,
) (ports.GroupPricing, error) {
	buyer, code, items := request.BuyerID, request.PromoCode, request.Items
	checks, err := p.checkItems(ctx, tenant, ports.GroupPricingRequest{BuyerID: buyer, Items: items})
	if err != nil {
		return ports.GroupPricing{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	result, ok := p.promos[tenant]
	if code == "" && result.PromoCode != nil {
		result = ports.GroupPricing{}
	}
	if !ok && code != "" {
		message := "промокод не найден"
		result.PromoCode = &cart.PromoCode{Code: code, Message: &message}
	}
	if len(result.Items) == 0 {
		result.Items = checks
	}
	return result, nil
}
