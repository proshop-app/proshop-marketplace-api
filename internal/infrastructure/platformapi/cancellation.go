package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type cancellationJSON struct {
	Status  string          `json:"status"`
	OrderID json.RawMessage `json:"orderId"`
}

type cancellationConflictJSON struct {
	Code    string               `json:"code"`
	OrderID string               `json:"orderId"`
	Status  checkout.GroupStatus `json:"status"`
}

// CancelOrder использует ключ создания, в том числе когда ответ с orderId был потерян.
func (c *Client) CancelOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CancelOrderRequest,
) (ports.CancelOrderResult, error) {
	var answer cancellationJSON
	err := c.callWithRefusal(
		ctx,
		http.MethodPost,
		tenant,
		"/orders/cancel-by-checkout",
		request,
		&answer,
		cancellationRefusal,
	)
	if errors.Is(err, ErrInvalidResponse) {
		return ports.CancelOrderResult{}, ports.ErrInvalidCancellationResponse.Wrap(err)
	}
	if err != nil {
		return ports.CancelOrderResult{}, err
	}
	if answer.Status == "absent" && string(answer.OrderID) == "null" {
		return ports.CancelOrderResult{Absent: true}, nil
	}
	var id string
	if answer.Status != "cancelled" || json.Unmarshal(answer.OrderID, &id) != nil || id == "" {
		return ports.CancelOrderResult{}, ports.ErrInvalidCancellationResponse.Wrap(ErrInvalidResponse)
	}
	return ports.CancelOrderResult{OrderID: id}, nil
}

func cancellationRefusal(method, path string, tenant ids.TenantID, reply *http.Response) error {
	raw, err := io.ReadAll(io.LimitReader(reply.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return ErrInvalidResponse
	}
	var body cancellationConflictJSON
	if reply.StatusCode == http.StatusConflict && json.Unmarshal(raw, &body) == nil &&
		(body.Code == "" || body.Code == "ORDER_NOT_CANCELLABLE") {
		if body.OrderID == "" {
			return ErrInvalidResponse
		}
		switch body.Status {
		case checkout.GroupPaid, checkout.GroupShipped, checkout.GroupDelivered, checkout.GroupReturned:
			return &ports.OrderNotCancellable{OrderID: body.OrderID, Status: body.Status}
		default:
			return ErrInvalidResponse
		}
	}
	replay := *reply
	replay.Body = io.NopCloser(bytes.NewReader(raw))
	return refusal(method, path, tenant, &replay)
}
