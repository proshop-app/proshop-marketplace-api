package ports

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// ErrPriceChanged — магазин отклонил создание из-за актуальной цены.
var ErrPriceChanged = errs.New(errs.Conflict, "checkout.price_changed", "цена магазина изменилась")

// ErrOrderRefusal — магазин отклонил команду или сверку заказа.
var ErrOrderRefusal = errs.New(errs.Invalid, "platform.order_refusal", "магазин отклонил заказ")

// ErrUnknownOrderRefusal — неизвестный ответ требует ограниченного разбора и диагностики.
var ErrUnknownOrderRefusal = errs.New(errs.Internal, "platform.unknown_order_refusal", "неизвестный отказ магазина")

// ErrDispatchOriginChanged допускает повтор того же ключа после нового согласия покупателя.
var ErrDispatchOriginChanged = errs.New(errs.Conflict, "platform.dispatch_origin_changed", "склад отправки изменён")

// ErrOrderNotFound — исходного заказа больше нет на сервере.
var ErrOrderNotFound = errs.New(errs.NotFound, "platform.order_not_found", "заказ магазина не найден")

// ErrTemporaryOrderRefusal — отказ магазина допускает повтор после восстановления.
var ErrTemporaryOrderRefusal = errs.New(
	errs.Unavailable,
	"platform.temporary_order_refusal",
	"временный отказ магазина",
)

// ErrInvalidOrderStatus — сервер вернул статус вне словаря §4.
var ErrInvalidOrderStatus = errs.New(
	errs.Unavailable,
	"platform.invalid_order_status",
	"неизвестный статус заказа магазина",
)

// ErrInvalidCancellationResponse означает неисправимый ответ команды отмены.
var ErrInvalidCancellationResponse = errs.New(
	errs.Invalid,
	"platform.invalid_cancellation_response",
	"ответ отмены не соответствует контракту",
)

// PriceChanged содержит актуальные цены для повторного оформления, без изменения снимка заказа.
type PriceChanged struct{ Items []checkout.PriceChange }

// OrderItem — позиция с неизменной ценой маркетплейса при оформлении.
type OrderItem struct {
	Discounts []cart.Discount
	ProductID ids.ProductID
	Quantity  int
	Price     money.Money
}

// CreateOrderRequest — сохранённая команда магазина с ключом группы.
type CreateOrderRequest struct {
	PromoCode          string           `json:"promoCode,omitempty"`
	ExternalCheckoutID string           `json:"externalCheckoutId"`
	GroupID            ids.TenantID     `json:"groupId"`
	Items              []OrderItem      `json:"items"`
	Carrier            string           `json:"carrier"`
	PickupPointCode    string           `json:"pickupPointCode"`
	Contact            checkout.Contact `json:"contact"`
	DeliveryPrice      money.Money      `json:"deliveryPrice"`
}

// CancelOrderRequest — команда запрета создания и отмены заказа по ключу оформления.
type CancelOrderRequest struct {
	ExternalCheckoutID string       `json:"externalCheckoutId"`
	GroupID            ids.TenantID `json:"groupId"`
	Reason             string       `json:"reason"`
}

// CancelOrderResult — подтверждённая отмена либо запрет создания отсутствующего заказа.
type CancelOrderResult struct {
	OrderID string
	Absent  bool
}

// OrderNotCancellable сохраняет известный заказ для операторской сверки.
type OrderNotCancellable struct {
	OrderID string
	Status  checkout.GroupStatus
}

// SellerOrders — команды и сверка заказов через тот же доверенный канал.
type SellerOrders interface {
	CancelOrder(context.Context, ids.TenantID, CancelOrderRequest) (CancelOrderResult, error)
	// StubPayment идемпотентно подтверждает заказ болванкой; amount — сумма в копейках.
	StubPayment(ctx context.Context, tenantID ids.TenantID, orderID string, amount money.Money) error
	CreateOrder(context.Context, ids.TenantID, CreateOrderRequest) (checkout.SellerOrder, error)
	GetOrder(context.Context, ids.TenantID, string) (checkout.SellerOrder, error)
	// OrderStatuses — сверка пачкой: один запрос на страницу заказов магазина, без треков.
	OrderStatuses(ctx context.Context, tenantID ids.TenantID, orderIDs []string) ([]checkout.OrderStatus, error)
}

// OrderRefusal — код отказа магазина из словаря §4; неизвестный код сохраняется для оператора.
type OrderRefusal struct {
	DispatchReviewRevision int
	Code                   string
	Reason                 string
	Unexpected             bool
}

func (e *PriceChanged) Error() string { return ErrPriceChanged.Error() }
func (e *PriceChanged) Unwrap() error { return ErrPriceChanged }

func (e *OrderRefusal) Error() string { return "platform.order_refusal: " + e.Code }

func (e *OrderRefusal) Unwrap() error {
	if e.Unexpected {
		return ErrUnknownOrderRefusal
	}
	switch e.Code {
	case checkout.DispatchOriginChanged:
		return ErrDispatchOriginChanged
	case "ORDER_NOT_FOUND":
		return ErrOrderNotFound
	case "MARKETPLACE_TEMPORARY_FAILURE":
		return ErrTemporaryOrderRefusal
	case "DELIVERY_UNAVAILABLE", "DELIVERY_CHANGED", "OUT_OF_STOCK", "IDEMPOTENCY_CONFLICT",
		"ORDER_CHECKOUT_CANCELLED", "MARKETPLACE_STUB_PAYMENT_AMOUNT", "MARKETPLACE_STUB_PAYMENT_STATE":
		return ErrOrderRefusal
	default:
		return ErrUnknownOrderRefusal
	}
}

func (e *OrderNotCancellable) Error() string {
	return "platform.order_not_cancellable: " + string(e.Status)
}
func (e *OrderNotCancellable) Unwrap() error { return ErrOrderRefusal }
