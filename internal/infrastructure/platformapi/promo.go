package platformapi

import (
	"context"
	"net/http"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type groupPricingJSON struct {
	TenantID  ids.TenantID    `json:"tenantId"`
	PromoCode *promoCodeJSON  `json:"promoCode"`
	Items     []itemCheckJSON `json:"items"`
}

// PriceGroup — проверяет код и строки одной группы магазина.
func (c *Client) PriceGroup(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.GroupPricingRequest,
) (ports.GroupPricing, error) {
	buyer, code, items := request.BuyerID, request.PromoCode, request.Items
	var answer struct {
		Items []groupPricingJSON `json:"items"`
	}
	body := map[string]any{
		"buyerId":   buyer,
		"tenantIds": []ids.TenantID{tenant},
		"items":     itemsJSON(items),
	}
	if code != "" {
		body["promoCode"] = code
	}
	if err := c.call(ctx, http.MethodPost, "", "/pricing/contexts", body, &answer, tenant); err != nil {
		return ports.GroupPricing{}, err
	}
	if len(answer.Items) != 1 || answer.Items[0].TenantID != tenant {
		return ports.GroupPricing{}, ErrInvalidResponse
	}
	group := answer.Items[0]
	if code != "" && group.PromoCode == nil {
		return ports.GroupPricing{}, ErrInvalidResponse
	}
	if group.PromoCode != nil && (group.PromoCode.Code == "" || group.PromoCode.Discount < 0 ||
		(!group.PromoCode.Valid && group.PromoCode.Message == nil)) {
		return ports.GroupPricing{}, ErrInvalidResponse
	}
	result := ports.GroupPricing{
		PromoCode: group.PromoCode.domain(),
		Discounts: make(map[ids.ProductID][]cart.Discount),
	}
	quantities := make(map[ids.ProductID]int, len(items))
	for _, item := range items {
		quantities[item.ProductID] = item.Quantity
	}
	for _, item := range group.Items {
		id := ids.ProductID(item.ProductID)
		quantity, ok := quantities[id]
		if !ok || item.Price < 0 || item.AvailableQuantity < 0 {
			return ports.GroupPricing{}, ErrInvalidResponse
		}
		delete(quantities, id)
		price := money.FromKopecks(item.Price)
		discounts := domainDiscounts(item.Discounts)
		if _, err := cart.DiscountedAmount(price, quantity, discounts); err != nil {
			return ports.GroupPricing{}, ErrInvalidResponse
		}
		result.Items = append(result.Items, ports.ItemCheck{
			ProductID: id,
			Exists:    item.Available,
			Available: item.AvailableQuantity,
			Price:     price,
		})
		result.Discounts[id] = discounts
	}
	if len(quantities) != 0 {
		return ports.GroupPricing{}, ErrInvalidResponse
	}
	return result, nil
}
