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
	return call(
		ctx,
		c,
		callerClass(ctx),
		"price_group",
		c.cfg.StrictTimeout,
		func(ctx context.Context) (ports.GroupPricing, error) {
			return c.upstream.PriceGroup(ctx, tenant, request)
		},
	)
}

// MarketplaceSettings не использует витринный кэш при сверке участия и оформлении.
func (c *Client) MarketplaceSettings(ctx context.Context, tenant ids.TenantID) (ports.ShopSettings, error) {
	ctx = context.WithValue(ctx, tenantContextKey{}, tenant)
	return call(
		ctx,
		c,
		callerClass(ctx),
		"settings",
		c.cfg.StrictTimeout,
		func(ctx context.Context) (ports.ShopSettings, error) {
			return c.upstream.MarketplaceSettings(ctx, tenant)
		},
	)
}

// QuoteDelivery ограничивает время расчёта без подмены ответа магазина.
func (c *Client) QuoteDelivery(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.QuoteRequest,
) (delivery.Offered, error) {
	ctx = context.WithValue(ctx, tenantContextKey{}, tenant)
	return call(
		ctx,
		c,
		checkoutClass,
		"quote",
		c.cfg.StrictTimeout,
		func(ctx context.Context) (delivery.Offered, error) {
			return c.upstream.QuoteDelivery(ctx, tenant, request)
		},
	)
}

// CreateOrder не повторяет и не кэширует команду создания заказа.
func (c *Client) CreateOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	ctx = context.WithValue(ctx, tenantContextKey{}, tenant)
	return call(
		ctx,
		c,
		checkoutClass,
		"create_order",
		c.cfg.StrictTimeout,
		func(ctx context.Context) (checkout.SellerOrder, error) {
			return c.upstream.CreateOrder(ctx, tenant, request)
		},
	)
}

// GetOrder сохраняет ошибки сверки, чтобы состояние покупки не становилось выдуманным.
func (c *Client) GetOrder(ctx context.Context, tenant ids.TenantID, order string) (checkout.SellerOrder, error) {
	ctx = context.WithValue(ctx, tenantContextKey{}, tenant)
	return call(
		ctx,
		c,
		checkoutClass,
		"get_order",
		c.cfg.StrictTimeout,
		func(ctx context.Context) (checkout.SellerOrder, error) {
			return c.upstream.GetOrder(ctx, tenant, order)
		},
	)
}

// StubPayment использует тот же общий лимит, что остальные обращения к ячейке.
func (c *Client) StubPayment(ctx context.Context, tenant ids.TenantID, order string, amount money.Money) error {
	ctx = context.WithValue(ctx, tenantContextKey{}, tenant)
	_, err := call(
		ctx,
		c,
		checkoutClass,
		"stub_payment",
		c.cfg.StrictTimeout,
		func(ctx context.Context) (struct{}, error) {
			return struct{}{}, c.upstream.StubPayment(ctx, tenant, order, amount)
		},
	)
	return err
}

// PickupPoints ограничивает время запроса списка пунктов без подмены ответа магазина.
func (c *Client) PickupPoints(
	ctx context.Context,
	tenant ids.TenantID,
	carrier delivery.Carrier,
	city string,
) (ports.PickupPoints, error) {
	ctx = context.WithValue(ctx, tenantContextKey{}, ids.TenantID(""))
	class := catalogClass
	if ports.PlatformClassFromContext(ctx) == ports.PlatformCheckout {
		class = checkoutCatalogClass
	}
	return call(
		ctx,
		c,
		class,
		"pickup_points",
		c.cfg.StrictTimeout,
		func(ctx context.Context) (ports.PickupPoints, error) {
			answer, err := c.upstream.PickupPoints(ctx, tenant, carrier, city)
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				return answer, &ports.PickupCatalogFailure{Cause: err}
			}
			return answer, nil
		},
	)
}

// CancelOrder ограничивает команду тем же бюджетом и учитывает метрики магазина.
func (c *Client) CancelOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CancelOrderRequest,
) (ports.CancelOrderResult, error) {
	ctx = context.WithValue(ctx, tenantContextKey{}, tenant)
	cancelOrder := func(ctx context.Context) (ports.CancelOrderResult, error) {
		return c.upstream.CancelOrder(ctx, tenant, request)
	}
	return call(ctx, c, checkoutClass, "cancel_order", c.cfg.StrictTimeout, cancelOrder)
}
