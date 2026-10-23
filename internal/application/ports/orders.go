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

func (e *PriceChanged) Error() string { return ErrPriceChanged.Error() }
func (e *PriceChanged) Unwrap() error { return ErrPriceChanged }
