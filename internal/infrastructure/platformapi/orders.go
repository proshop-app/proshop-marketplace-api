package platformapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type sellerOrderJSON struct {
	Carrier         string                  `json:"carrier"`
	PickupPointCode string                  `json:"pickupPointCode"`
	Tracking        *trackingJSON           `json:"tracking"`
	OrderID         string                  `json:"orderId"`
	ExpiresAt       time.Time               `json:"expiresAt"`
	Total           *int64                  `json:"total"`
	Status          checkout.GroupStatus    `json:"status"`
	Reason          string                  `json:"reason"`
	Revision        int64                   `json:"revision"`
	Number          string                  `json:"number"`
	Steps           []checkout.OrderStep    `json:"steps"`
	ReturnRequest   *checkout.ReturnRequest `json:"returnRequest"`
}

type orderStatusesJSON struct {
	Items []orderStatusJSON `json:"items"`
}

type orderStatusJSON struct {
	OrderID       string                  `json:"orderId"`
	Number        string                  `json:"number"`
	Steps         []checkout.OrderStep    `json:"steps"`
	ReturnRequest *checkout.ReturnRequest `json:"returnRequest"`
	Status        checkout.GroupStatus    `json:"status"`
	Revision      int64                   `json:"revision"`
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

// OrderStatuses читает статусы заказов магазина одним запросом; неизвестного магазину заказа в ответе нет.
func (c *Client) OrderStatuses(
	ctx context.Context,
	tenant ids.TenantID,
	orderIDs []string,
) ([]checkout.OrderStatus, error) {
	var answer orderStatusesJSON
	body := map[string][]string{"orderIds": orderIDs}
	if err := c.call(ctx, http.MethodPost, tenant, "/orders/statuses", body, &answer); err != nil {
		return nil, err
	}
	statuses := make([]checkout.OrderStatus, 0, len(answer.Items))
	for _, item := range answer.Items {
		if item.OrderID == "" || item.Revision < 0 {
			return nil, ErrInvalidResponse
		}
		// Заказ со статусом вне словаря остаётся для сверки неизвестным: остальные заказы магазина сверяются.
		if !checkout.ShopOrderStatus(item.Status) {
			slog.Warn(
				"неизвестный статус заказа магазина",
				slog.String("tenantId", string(tenant)),
				slog.String("status", string(item.Status)),
			)
			continue
		}
		statuses = append(statuses, checkout.OrderStatus(item))
	}
	return statuses, nil
}

func (a sellerOrderJSON) order(creating bool) (checkout.SellerOrder, error) {
	if !checkout.ShopOrderStatus(a.Status) {
		return checkout.SellerOrder{}, ports.ErrInvalidOrderStatus
	}
	if a.OrderID == "" || a.Revision < 0 || (a.Status == checkout.GroupCancelled && a.Reason == "") {
		return checkout.SellerOrder{}, ErrInvalidResponse
	}
	if creating && (a.Total == nil || *a.Total < 0 || a.ExpiresAt.IsZero()) {
		return checkout.SellerOrder{}, ErrInvalidResponse
	}
	order := checkout.SellerOrder{
		Carrier:         delivery.Carrier(a.Carrier),
		PickupPointCode: a.PickupPointCode,
		OrderID:         a.OrderID,
		ExpiresAt:       a.ExpiresAt,
		Status:          a.Status,
		Reason:          a.Reason,
		Revision:        a.Revision,
		Number:          a.Number,
		Steps:           a.Steps,
		ReturnRequest:   a.ReturnRequest,
		Tracking:        a.Tracking.domain(),
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
