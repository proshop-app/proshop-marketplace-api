package platformapi_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func orderRequest() ports.CreateOrderRequest {
	return ports.CreateOrderRequest{
		ExternalCheckoutID: "checkout1:t1",
		GroupID:            "t1",
		Items:              []ports.OrderItem{{ProductID: "p1", Quantity: 2, Price: money.FromKopecks(199000)}},
		Carrier:            "cdek",
		PickupPointCode:    "P1",
		Contact:            checkout.Contact{Phone: "79251234567", Name: "Покупатель"},
		DeliveryPrice:      money.FromKopecks(15000),
		ReturnURL:          "https://market.example/return",
	}
}

func TestOrderContract(t *testing.T) {
	client := server(
		t,
		map[string][2]string{
			"POST " + prefix + "/orders":   {"order_create_request.json", "order_created.json"},
			"GET " + prefix + "/orders/o1": {"", "order_shipped.json"},
		},
		http.StatusOK,
	)
	order, err := client.CreateOrder(context.Background(), "t1", orderRequest())
	if err != nil || order.OrderID != "o1" || order.Total.Kopecks() != 413000 ||
		order.Status != checkout.GroupAwaitingPayment || order.ExpiresAt.IsZero() {
		t.Fatalf("создание: %+v %v", order, err)
	}
	status, err := client.GetOrder(context.Background(), "t1", order.OrderID)
	if err != nil || status.Status != checkout.GroupShipped || status.Track != "TRACK1" {
		t.Fatalf("статус: %+v %v", status, err)
	}
}

func TestOrderPriceChangedContract(t *testing.T) {
	client := server(
		t,
		map[string][2]string{
			"POST " + prefix + "/orders": {"order_create_request.json", "order_price_changed.json"},
		},
		http.StatusConflict,
	)
	_, err := client.CreateOrder(context.Background(), "t1", orderRequest())
	var changed *ports.PriceChanged
	if !errors.Is(err, ports.ErrPriceChanged) || !errors.As(err, &changed) ||
		len(changed.Items) != 1 || changed.Items[0].Price.Kopecks() != 209000 {
		t.Fatalf("цены: %v", err)
	}
}

func TestSettingsRequireDeliveryAndMinimum(t *testing.T) {
	client := server(
		t,
		map[string][2]string{
			"GET " + prefix + "/settings/marketplace": {"", "settings_missing_delivery.json"},
		},
		http.StatusOK,
	)
	if _, err := client.MarketplaceSettings(context.Background(), "t1"); !errors.Is(err, ports.ErrInvalidSettings) {
		t.Fatalf("обязательные настройки: %v", err)
	}
}

func TestOrderRefusalDictionary(t *testing.T) {
	for _, tc := range []struct {
		fixture    string
		code       string
		status     int
		unexpected bool
	}{
		{"order_delivery_unavailable.json", "DELIVERY_UNAVAILABLE", http.StatusConflict, false},
		{"order_delivery_changed.json", "DELIVERY_CHANGED", http.StatusConflict, false},
		{"order_out_of_stock.json", "OUT_OF_STOCK", http.StatusConflict, false},
		{"order_idempotency_conflict.json", "IDEMPOTENCY_CONFLICT", http.StatusConflict, false},
		{"order_not_found.json", "ORDER_NOT_FOUND", http.StatusNotFound, false},
		{"order_unknown_error.json", "NEW_FAILURE", http.StatusConflict, true},
		{"order_delivery_unavailable.json", "DELIVERY_UNAVAILABLE", http.StatusInternalServerError, true},
	} {
		t.Run(tc.code, func(t *testing.T) {
			client := server(
				t,
				map[string][2]string{"POST " + prefix + "/orders": {"", tc.fixture}},
				tc.status,
			)
			_, err := client.CreateOrder(context.Background(), "t1", orderRequest())
			var refusal *ports.OrderRefusal
			if !errors.As(err, &refusal) || refusal.Code != tc.code || refusal.Unexpected != tc.unexpected {
				t.Fatalf("код отказа потерян: %+v", err)
			}
			if tc.code == "DELIVERY_UNAVAILABLE" && refusal.Reason != "pickup_point_unavailable" {
				t.Fatal("потеряна причина недоступности ПВЗ")
			}
		})
	}
}

func TestUnknownOrderStatusIsTypedFailure(t *testing.T) {
	client := server(
		t,
		map[string][2]string{"GET " + prefix + "/orders/o1": {"", "order_unknown_status.json"}},
		http.StatusOK,
	)
	if _, err := client.GetOrder(context.Background(), "t1", "o1"); !errors.Is(err, ports.ErrInvalidOrderStatus) {
		t.Fatalf("неизвестный статус должен запускать задержку опроса: %v", err)
	}
}
