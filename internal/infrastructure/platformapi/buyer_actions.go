package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const maxActionRefusalBody = 4 << 20

// CancelPaidOrder отменяет оплаченный заказ от имени покупателя.
func (c *Client) CancelPaidOrder(
	ctx context.Context,
	tenant ids.TenantID,
	id string,
) (checkout.SellerOrder, error) {
	return c.buyerAction(ctx, tenant, id, "/cancel", struct{}{})
}

// RequestReturn подаёт заявку покупателя на возврат.
func (c *Client) RequestReturn(
	ctx context.Context,
	tenant ids.TenantID,
	id string,
	reason checkout.ReturnReason,
) (checkout.SellerOrder, error) {
	return c.buyerAction(ctx, tenant, id, "/return-request", map[string]string{"reason": string(reason)})
}

func (c *Client) buyerAction(
	ctx context.Context,
	tenant ids.TenantID,
	id, action string,
	body any,
) (checkout.SellerOrder, error) {
	var answer sellerOrderJSON
	path := "/orders/" + url.PathEscape(id) + action
	if err := c.callWithRefusal(ctx, http.MethodPost, tenant, path, body, &answer, actionRefusal); err != nil {
		return checkout.SellerOrder{}, err
	}
	if answer.OrderID != id {
		return checkout.SellerOrder{}, ErrInvalidResponse
	}
	return answer.order(false)
}

// actionRefusal переводит отказы действий покупателя; остальные разбирает общий словарь.
func actionRefusal(method, path string, tenant ids.TenantID, reply *http.Response) error {
	raw, err := io.ReadAll(io.LimitReader(reply.Body, maxActionRefusalBody+1))
	if err != nil || len(raw) > maxActionRefusalBody {
		return ErrInvalidResponse
	}
	var body errorJSON
	if reply.StatusCode == http.StatusConflict && json.Unmarshal(raw, &body) == nil {
		switch body.Code {
		case "ORDER_NOT_CANCELLABLE":
			return checkout.ErrOrderNotCancellable
		case "ORDER_RETURN_UNAVAILABLE":
			return checkout.ErrReturnUnavailable
		}
	}
	replay := *reply
	replay.Body = io.NopCloser(bytes.NewReader(raw))
	return refusal(method, path, tenant, &replay)
}
