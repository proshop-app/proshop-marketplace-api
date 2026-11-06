package ports

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// ErrMarketplaceDisabled — сервер отказал: магазин не участвует в маркетплейсе
// (403 MARKETPLACE_DISABLED). Для покупателя это недоступность магазина.
var ErrMarketplaceDisabled = errs.New(
	errs.Unavailable,
	"platform.marketplace_disabled",
	"магазин не участвует в маркетплейсе",
)

// ErrInvalidSettings — отсутствие обязательных настроек скрывает участника.
var ErrInvalidSettings = errs.New(errs.Invalid, "platform.invalid_settings", "настройки участия не по контракту")

// ItemRequest — позиция заказа продавца.
type ItemRequest struct {
	ProductID ids.ProductID
	Quantity  int
}

// ItemsCheckRequest привязывает проверку к аккаунту, а не контакту получателя.
type ItemsCheckRequest struct {
	BuyerID ids.UUID
	Items   []ItemRequest
}

// ItemCheck — ответ магазина по позиции: продаёт ли, по какой цене, сколько есть.
type ItemCheck struct {
	ProductID ids.ProductID
	Exists    bool
	Price     money.Money
	Available int
}

// QuoteRequest — расчёт доставки заказа продавца до пункта выдачи.
type QuoteRequest struct {
	BuyerID         ids.UUID
	Carrier         delivery.Carrier
	PickupPointCode string
	City            string
	Items           []ItemRequest
}

// ShopSettings — настройки магазина для маркетплейса.
type ShopSettings struct {
	Profile          participant.Profile
	Card             participant.CardReadiness
	Returns          *participant.Returns
	Subsidy          participant.Subsidy
	FreeDeliveryFrom money.Money
	Delivery         participant.DeliverySettings
}

// CatalogPricing — выбор профилей до фильтрации и пагинации каталога.
type CatalogPricing interface {
	PriceContexts(context.Context, ids.UUID, []ids.TenantID) (map[ids.TenantID]string, error)
}

// PlatformAPI — доверенный канал продавца; цены выбираются по проверенному аккаунту.
type PlatformAPI interface {
	SellerOrders
	CatalogPricing
	// CheckItems — цена и наличие позиций у магазина сейчас.
	CheckItems(ctx context.Context, tenantID ids.TenantID, request ItemsCheckRequest) ([]ItemCheck, error)
	// QuoteDelivery — стоимость и срок доставки по договору магазина.
	QuoteDelivery(ctx context.Context, tenantID ids.TenantID, request QuoteRequest) (delivery.Offered, error)
	// MarketplaceSettings — обязательные настройки доставки магазина.
	MarketplaceSettings(ctx context.Context, tenantID ids.TenantID) (ShopSettings, error)
}
