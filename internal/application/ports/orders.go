package ports

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// ErrPriceChanged — магазин отклонил создание из-за актуальной цены.
var ErrPriceChanged = errs.New(errs.Conflict, "checkout.price_changed", "цена магазина изменилась")

// ErrOrderRefusal — магазин отклонил команду или сверку заказа.
var ErrOrderRefusal = errs.New(errs.Unavailable, "platform.order_refusal", "магазин отклонил заказ")

// ErrInvalidOrderStatus — сервер вернул статус вне словаря §4.
var ErrInvalidOrderStatus = errs.New(
	errs.Unavailable,
	"platform.invalid_order_status",
	"неизвестный статус заказа магазина",
)

// PriceChanged содержит актуальные цены для повторного оформления, без изменения снимка заказа.
type PriceChanged struct{ Items []checkout.PriceChange }

// OrderItem — позиция с неизменной ценой маркетплейса при оформлении.
type OrderItem struct {
	ProductID ids.ProductID `json:"productId"`
	Quantity  int           `json:"quantity"`
	Price     money.Money   `json:"price"`
}

// CreateOrderRequest — сохранённая команда магазина с ключом группы.
type CreateOrderRequest struct {
	BuyerID            ids.UUID         `json:"buyerId,omitempty"`
	ExternalCheckoutID string           `json:"externalCheckoutId"`
	GroupID            ids.TenantID     `json:"groupId"`
	Items              []OrderItem      `json:"items"`
	Carrier            string           `json:"carrier"`
	PickupPointCode    string           `json:"pickupPointCode"`
	Contact            checkout.Contact `json:"contact"`
	DeliveryPrice      money.Money      `json:"deliveryPrice"`
	ReturnURL          string           `json:"returnUrl"`
}

// SellerOrders — команды и сверка заказов через тот же доверенный канал.
type SellerOrders interface {
	CreateOrder(context.Context, ids.TenantID, CreateOrderRequest) (checkout.SellerOrder, error)
	GetOrder(context.Context, ids.TenantID, string) (checkout.SellerOrder, error)
}

// OrderRefusal — код отказа магазина из словаря §4; неизвестный код сохраняется для оператора.
type OrderRefusal struct {
	Code       string
	Reason     string
	Unexpected bool
}

func (e *PriceChanged) Error() string { return ErrPriceChanged.Error() }
func (e *PriceChanged) Unwrap() error { return ErrPriceChanged }

func (e *OrderRefusal) Error() string { return "platform.order_refusal: " + e.Code }

func (e *OrderRefusal) Unwrap() error { return ErrOrderRefusal }
