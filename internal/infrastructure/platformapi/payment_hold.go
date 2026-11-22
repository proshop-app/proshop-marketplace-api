package platformapi

import (
	"context"
	"net/http"
	"net/url"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// PaymentHold запрашивает однократное удержание, возвращая сохранённое состояние магазина.
func (c *Client) PaymentHold(ctx context.Context, tenant ids.TenantID, id string) (checkout.SellerOrder, error) {
	var answer sellerOrderJSON
	path := "/orders/" + url.PathEscape(id) + "/payment-hold"
	if err := c.call(ctx, http.MethodPost, tenant, path, struct{}{}, &answer); err != nil {
		return checkout.SellerOrder{}, err
	}
	order, err := answer.order(false)
	if err != nil {
		return checkout.SellerOrder{}, err
	}
	if order.OrderID != id || !order.PaymentHoldConfirmed() {
		return checkout.SellerOrder{}, ErrInvalidResponse
	}
	return order, nil
}
