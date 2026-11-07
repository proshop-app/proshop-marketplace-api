package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestOrderIdempotencyComparesRequestBody(t *testing.T) {
	platform := fake.NewPlatform()
	request := ports.CreateOrderRequest{
		ExternalCheckoutID: "checkout:t1",
		GroupID:            "t1",
		Items:              []ports.OrderItem{{ProductID: "p1", Quantity: 1, Price: money.FromKopecks(100)}},
		DeliveryPrice:      money.FromKopecks(10),
	}
	original, err := platform.CreateOrder(context.Background(), "t1", request)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := platform.CreateOrder(context.Background(), "t1", request)
	if err != nil || original.OrderID != repeated.OrderID {
		t.Fatal(err)
	}
	for _, modify := range []func(*ports.CreateOrderRequest){
		func(r *ports.CreateOrderRequest) { r.DeliveryPrice = money.FromKopecks(20) },
		func(r *ports.CreateOrderRequest) { r.Items[0].Quantity++ },
		func(r *ports.CreateOrderRequest) { r.Contact.Name = "Другой покупатель" },
		func(r *ports.CreateOrderRequest) { r.PickupPointCode = "other" },
	} {
		altered := request
		altered.Items = append([]ports.OrderItem(nil), request.Items...)
		modify(&altered)
		_, err := platform.CreateOrder(context.Background(), "t1", altered)
		var refusal *ports.OrderRefusal
		if !errors.As(err, &refusal) || refusal.Code != "IDEMPOTENCY_CONFLICT" {
			t.Fatalf("изменённое тело: %v", err)
		}
	}
	if platform.OrderCount() != 1 {
		t.Fatal("повторы не должны создавать новые заказы")
	}
}
