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
	PromoCode string
	Items     []ItemRequest
}

// PromoPricing — проверка промокода, публичной цены и наличия.
type PromoPricing interface {
	PriceGroup(context.Context, ids.TenantID, GroupPricingRequest) (GroupPricing, error)
}
