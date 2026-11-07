package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ItemCheckCall сохраняет проверенный аккаунт каждого обращения к продавцу.
type ItemCheckCall struct {
	TenantID ids.TenantID
	Request  ports.GroupPricingRequest
}

// PriceContexts возвращает выбор профиля независимо для каждого покупателя и магазина.
func (p *Platform) PriceContexts(
	_ context.Context,
	buyerID ids.UUID,
	tenants []ids.TenantID,
) (map[ids.TenantID]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	profiles := make(map[ids.TenantID]string, len(tenants))
	for _, tenant := range tenants {
		if err := p.failing[tenant]; err != nil {
			return nil, err
		}
		profiles[tenant] = catalog.DefaultPriceProfile
		if selected := p.buyerProfiles[buyerID][tenant]; selected != "" {
			profiles[tenant] = selected
		}
	}
	return profiles, nil
}

// SetBuyerProfile задаёт назначение ценового профиля аккаунту в тесте.
func (p *Platform) SetBuyerProfile(buyerID ids.UUID, tenant ids.TenantID, key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.buyerProfiles == nil {
		p.buyerProfiles = map[ids.UUID]map[ids.TenantID]string{}
	}
	if p.buyerProfiles[buyerID] == nil {
		p.buyerProfiles[buyerID] = map[ids.TenantID]string{}
	}
	p.buyerProfiles[buyerID][tenant] = key
}

// SetBuyerItem задаёт текущую применимую цену конкретного покупателя.
func (p *Platform) SetBuyerItem(buyerID ids.UUID, tenant ids.TenantID, check ports.ItemCheck) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.buyerItems == nil {
		p.buyerItems = map[ids.UUID]map[ids.TenantID]map[ids.ProductID]ports.ItemCheck{}
	}
	if p.buyerItems[buyerID] == nil {
		p.buyerItems[buyerID] = map[ids.TenantID]map[ids.ProductID]ports.ItemCheck{}
	}
	if p.buyerItems[buyerID][tenant] == nil {
		p.buyerItems[buyerID][tenant] = map[ids.ProductID]ports.ItemCheck{}
	}
	p.buyerItems[buyerID][tenant][check.ProductID] = check
}

// ItemCheckCalls возвращает копию истории для проверки границы аккаунта.
func (p *Platform) ItemCheckCalls() []ItemCheckCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ItemCheckCall(nil), p.checks...)
}
