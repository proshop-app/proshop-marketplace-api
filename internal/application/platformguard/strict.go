package platformguard

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// PriceGroup всегда проверяет актуальные цены и наличие; кэш здесь недопустим.
func (c *Client) PriceGroup(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.GroupPricingRequest,
) (ports.GroupPricing, error) {
	ctx = context.WithValue(ctx, tenantContextKey{}, tenant)
	return call(ctx, c, "price_group", c.cfg.StrictTimeout, func(ctx context.Context) (ports.GroupPricing, error) {
		return c.upstream.PriceGroup(ctx, tenant, request)
	})
}

// MarketplaceSettings не использует витринный кэш при сверке участия и оформлении.
func (c *Client) MarketplaceSettings(ctx context.Context, tenant ids.TenantID) (ports.ShopSettings, error) {
	ctx = context.WithValue(ctx, tenantContextKey{}, tenant)
	return call(ctx, c, "settings", c.cfg.StrictTimeout, func(ctx context.Context) (ports.ShopSettings, error) {
		return c.upstream.MarketplaceSettings(ctx, tenant)
	})
}

// QuoteDelivery ограничивает время расчёта без подмены ответа магазина.
func (c *Client) QuoteDelivery(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.QuoteRequest,
) (delivery.Offered, error) {
	ctx = context.WithValue(ctx, tenantContextKey{}, tenant)
	return call(ctx, c, "quote", c.cfg.StrictTimeout, func(ctx context.Context) (delivery.Offered, error) {
		return c.upstream.QuoteDelivery(ctx, tenant, request)
	})
}

// CreateOrder не повторяет и не кэширует команду создания заказа.
func (c *Client) CreateOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	ctx = context.WithValue(ctx, tenantContextKey{}, tenant)
	return call(ctx, c, "create_order", c.cfg.StrictTimeout, func(ctx context.Context) (checkout.SellerOrder, error) {
		return c.upstream.CreateOrder(ctx, tenant, request)
	})
}

// GetOrder сохраняет ошибки сверки, чтобы состояние покупки не становилось выдуманным.
func (c *Client) GetOrder(ctx context.Context, tenant ids.TenantID, order string) (checkout.SellerOrder, error) {
	ctx = context.WithValue(ctx, tenantContextKey{}, tenant)
	return call(ctx, c, "get_order", c.cfg.StrictTimeout, func(ctx context.Context) (checkout.SellerOrder, error) {
		return c.upstream.GetOrder(ctx, tenant, order)
	})
}

// StubPayment использует тот же общий лимит, что остальные обращения к ячейке.
func (c *Client) StubPayment(ctx context.Context, tenant ids.TenantID, order string, amount money.Money) error {
	ctx = context.WithValue(ctx, tenantContextKey{}, tenant)
	_, err := call(ctx, c, "stub_payment", c.cfg.StrictTimeout, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, c.upstream.StubPayment(ctx, tenant, order, amount)
	})
	return err
}

// PickupPoints ограничивает время запроса списка пунктов без подмены ответа магазина.
func (c *Client) PickupPoints(
	ctx context.Context,
	tenant ids.TenantID,
	carrier delivery.Carrier,
	city string,
) (ports.PickupPoints, error) {
	ctx = context.WithValue(ctx, tenantContextKey{}, tenant)
	return call(ctx, c, "pickup_points", c.cfg.StrictTimeout, func(ctx context.Context) (ports.PickupPoints, error) {
		return c.upstream.PickupPoints(ctx, tenant, carrier, city)
	})
}
