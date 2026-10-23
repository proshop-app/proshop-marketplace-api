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

// ItemRequest — позиция заказа продавца.
type ItemRequest struct {
	ProductID ids.ProductID
	Quantity  int
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
	Carrier         delivery.Carrier
	PickupPointCode string
	City            string
	Items           []ItemRequest
}

// ShopSettings — настройки магазина для маркетплейса.
type ShopSettings struct {
	Enabled bool
	Subsidy participant.Subsidy
}

// PlatformAPI — внутренний API сервера установки в контексте магазина
// (контракт PR-M1, docs/server-contract.md). Магазин, который не участвует в
// маркетплейсе, — ErrMarketplaceDisabled.
type PlatformAPI interface {
	// CheckItems — цена и наличие позиций у магазина сейчас.
	CheckItems(ctx context.Context, tenantID ids.TenantID, items []ItemRequest) ([]ItemCheck, error)
	// QuoteDelivery — стоимость и срок доставки по договору магазина.
	QuoteDelivery(ctx context.Context, tenantID ids.TenantID, request QuoteRequest) (delivery.Offered, error)
	// MarketplaceSettings — флаг участия и субсидия из настроек магазина.
	MarketplaceSettings(ctx context.Context, tenantID ids.TenantID) (ShopSettings, error)
}
