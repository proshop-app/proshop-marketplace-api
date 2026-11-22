package platformapi_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

func TestBuyerActionsContract(t *testing.T) {
	routes := map[string][2]string{
		"POST " + prefix + "/orders/o1/cancel": {
			"order_action_cancel_request.json",
			"order_action_cancelled.json",
		},
		"POST " + prefix + "/orders/o1/return-request": {
			"order_action_return_request.json",
			"order_action_returned.json",
		},
	}
	client := server(t, routes, http.StatusOK)
	cancelled, err := client.CancelPaidOrder(context.Background(), "t1", "o1")
	if err != nil || cancelled.Reason != checkout.CancelledBeforeShipment || cancelled.Revision != 3 {
		t.Fatalf("отмена: %+v %v", cancelled, err)
	}
	requested, err := client.RequestReturn(context.Background(), "t1", "o1", checkout.ReturnDefective)
	if err != nil || requested.ReturnRequest == nil || requested.ReturnRequest.Status != checkout.ReturnRequested {
		t.Fatalf("заявка: %+v %v", requested, err)
	}
}

func TestBuyerActionsRefusals(t *testing.T) {
	routes := map[string][2]string{
		"POST " + prefix + "/orders/o1/cancel":         {"", "order_action_cancel_refused.json"},
		"POST " + prefix + "/orders/o1/return-request": {"", "order_action_return_refused.json"},
	}
	client := server(t, routes, http.StatusConflict)
	if _, err := client.CancelPaidOrder(context.Background(), "t1", "o1"); !errors.Is(
		err,
		checkout.ErrOrderNotCancellable,
	) {
		t.Fatalf("уехавший заказ: %v", err)
	}
	if _, err := client.RequestReturn(context.Background(), "t1", "o1", checkout.ReturnDefective); !errors.Is(
		err,
		checkout.ErrReturnUnavailable,
	) {
		t.Fatalf("решённая заявка: %v", err)
	}
}
