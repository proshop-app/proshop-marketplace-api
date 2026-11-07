package ports

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// GroupPricing — цены, скидки и результат проверки группы магазина.
type GroupPricing struct {
	PromoCode *cart.PromoCode
	Items     []ItemCheck
	Discounts map[ids.ProductID][]cart.Discount
}

// GroupPricingRequest — снимок строк и кода одного магазина.
type GroupPricingRequest struct {
	BuyerID   ids.UUID
	PromoCode string
	Items     []ItemRequest
}

// PromoPricing — проверка промокода и цен в контексте покупателя.
type PromoPricing interface {
	PriceGroup(context.Context, ids.TenantID, GroupPricingRequest) (GroupPricing, error)
}
