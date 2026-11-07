package platformapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type sellerOrderJSON struct {
	Tracking  *trackingJSON        `json:"tracking"`
	OrderID   string               `json:"orderId"`
	ExpiresAt time.Time            `json:"expiresAt"`
	Total     *int64               `json:"total"`
	Status    checkout.GroupStatus `json:"status"`
	Reason    string               `json:"reason"`
}

// CreateOrder создаёт заказ магазина без открытия кассы по ключу покупки и группы.
func (c *Client) CreateOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	var answer sellerOrderJSON
	if err := c.call(ctx, http.MethodPost, tenant, "/orders", orderBody(request), &answer); err != nil {
		return checkout.SellerOrder{}, err
	}
	if answer.Status == "" {
		answer.Status = checkout.GroupAwaitingPayment
	}
	return answer.order(true)
}

// GetOrder читает статус только по доверенному каналу этого магазина.
func (c *Client) GetOrder(ctx context.Context, tenant ids.TenantID, id string) (checkout.SellerOrder, error) {
	var answer sellerOrderJSON
	if err := c.call(ctx, http.MethodGet, tenant, "/orders/"+url.PathEscape(id), nil, &answer); err != nil {
		return checkout.SellerOrder{}, err
	}
	if answer.OrderID != id {
		return checkout.SellerOrder{}, ErrInvalidResponse
	}
	order, err := answer.order(false)
	if errors.Is(err, ports.ErrInvalidOrderStatus) {
		slog.Warn(
			"неизвестный статус заказа магазина",
			slog.String("tenantId", string(tenant)),
			slog.String("status", string(answer.Status)),
		)
	}
	return order, err
}

func (a sellerOrderJSON) order(creating bool) (checkout.SellerOrder, error) {
	switch a.Status {
	case checkout.GroupAwaitingPayment, checkout.GroupPaid, checkout.GroupCancelled,
		checkout.GroupShipped, checkout.GroupDelivered, checkout.GroupReturned:
	default:
		return checkout.SellerOrder{}, ports.ErrInvalidOrderStatus
	}
	if a.OrderID == "" || (a.Status == checkout.GroupCancelled && a.Reason == "") {
		return checkout.SellerOrder{}, ErrInvalidResponse
	}
	if creating && (a.Total == nil || *a.Total < 0 || a.ExpiresAt.IsZero()) {
		return checkout.SellerOrder{}, ErrInvalidResponse
	}
	if a.Tracking != nil && (strings.TrimSpace(a.Tracking.Carrier) == "" || !checkout.ValidURL(a.Tracking.URL)) {
		a.Tracking = nil
	}
	if a.Tracking != nil && a.Tracking.Number != nil && strings.TrimSpace(*a.Tracking.Number) == "" {
		a.Tracking.Number = nil
	}
	order := checkout.SellerOrder{
		OrderID:   a.OrderID,
		ExpiresAt: a.ExpiresAt,
		Status:    a.Status,
		Reason:    a.Reason,
		Tracking:  a.Tracking.domain(),
	}
	if a.Total != nil {
		order.Total = money.FromKopecks(*a.Total)
	}
	return order, nil
}

// StubPayment подтверждает заказ служебным платежом без движения денег.
func (c *Client) StubPayment(ctx context.Context, tenant ids.TenantID, id string, amount money.Money) error {
	var answer struct {
		Status string `json:"status"`
	}
	path := "/orders/" + url.PathEscape(id) + "/stub-payment"
	body := map[string]int64{"amount": amount.Kopecks()}
	if err := c.call(ctx, http.MethodPost, tenant, path, body, &answer); err != nil {
		return err
	}
	if answer.Status != "paid" {
		return ErrInvalidResponse
	}
	return nil
}
