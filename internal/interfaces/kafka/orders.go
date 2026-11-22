package kafka

import (
	"context"
	"strings"

	kafkago "github.com/segmentio/kafka-go"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// orderStatusVersion — версия договора события статуса заказа (docs/server-contract.md).
const orderStatusVersion = 1

// OrderWorkers — отдельная полоса статусов заказов: волна проекций каталога их не задерживает.
const OrderWorkers = 4

// Orders — приём состояний заказов магазинов.
type Orders interface {
	ApplyOrderEvent(ctx context.Context, event appcheckout.OrderEvent) error
}

type orderStatusEvent struct {
	Version            int                     `json:"version"`
	TenantID           string                  `json:"tenantId"`
	ExternalCheckoutID string                  `json:"externalCheckoutId"`
	OrderID            string                  `json:"orderId"`
	Status             checkout.GroupStatus    `json:"status"`
	Revision           int64                   `json:"revision"`
	Reason             string                  `json:"reason"`
	Number             string                  `json:"number"`
	Steps              []checkout.OrderStep    `json:"steps"`
	ReturnRequest      *checkout.ReturnRequest `json:"returnRequest"`
	Tracking           *orderTracking          `json:"tracking"`
}

type orderTracking struct {
	Carrier string  `json:"carrier"`
	Number  *string `json:"number"`
	URL     string  `json:"url"`
}

// orderStatusChanged — состояние заказа магазина: магазин в ключе и теле, заголовок — если есть.
func orderStatusChanged(ctx context.Context, orders Orders, envelope Envelope) error {
	var event orderStatusEvent
	if err := decode(envelope.Message, &event); err != nil {
		return err
	}
	keyTenant, keyOrder, err := projectionKey(envelope.Message)
	if err != nil {
		return err
	}
	if event.TenantID == "" || keyTenant == "" {
		return ErrTenantMissing
	}
	tenant, err := resolveTenant(keyTenant, event.TenantID, tenantHeader(envelope.Message))
	if err != nil {
		return err
	}
	if event.Version != orderStatusVersion || event.OrderID == "" || keyOrder != event.OrderID {
		return ErrInvalidPayload.WithMessage("версия контракта или заказ ключа не совпадают с телом")
	}
	if !checkout.ShopOrderStatus(event.Status) || event.Revision < 1 {
		return ErrInvalidPayload.WithMessage("статус или номер перехода заказа вне договора")
	}
	checkoutID, err := orderCheckout(event.ExternalCheckoutID, tenant)
	if err != nil {
		return err
	}

	return orders.ApplyOrderEvent(ctx, appcheckout.OrderEvent{
		EventID:    envelope.EventID,
		TenantID:   tenant,
		CheckoutID: checkoutID,
		Order: checkout.SellerOrder{
			OrderID:       event.OrderID,
			Status:        event.Status,
			Reason:        event.Reason,
			Revision:      event.Revision,
			Number:        event.Number,
			Steps:         event.Steps,
			ReturnRequest: event.ReturnRequest,
			Tracking:      event.Tracking.domain(),
		},
	})
}

// orderCheckout разбирает ключ заказа у магазина <checkoutId>:<tenantId>: покупку площадка находит
// по первичному ключу, без поиска по номеру заказа.
func orderCheckout(external string, tenant ids.TenantID) (ids.UUID, error) {
	id, owner, found := strings.Cut(external, ":")
	if !found || owner != string(tenant) {
		return "", ErrInvalidPayload.WithMessage("ключ оформления не принадлежит магазину события")
	}
	checkoutID, err := ids.ParseUUID(id)
	if err != nil {
		return "", ErrInvalidPayload.Wrap(err)
	}
	return checkoutID, nil
}

func (t *orderTracking) domain() *checkout.Tracking {
	if t == nil {
		return nil
	}
	return checkout.NewTracking(t.Carrier, t.Number, t.URL)
}

// orderKey — очередь одного заказа: события разных заказов магазина идут параллельно.
func orderKey(message kafkago.Message) orderingKey {
	tenant, order, err := projectionKey(message)
	if err != nil || order == "" {
		return orderingKey{}
	}
	if _, err := ids.ParseTenantID(tenant); err != nil {
		return orderingKey{}
	}
	return orderingKey{kind: orderOrder, tenant: tenant, entity: order}
}
