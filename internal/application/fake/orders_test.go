package fake_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestOrderIdempotencyComparesRequestBody(t *testing.T) {
	platform := fake.NewPlatform()
	request := ports.CreateOrderRequest{
		PaymentDueAt:       platform.OrderNow.Add(10 * time.Minute),
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

func TestPaymentDueValidationAndIdempotentReplay(t *testing.T) {
	platform := fake.NewPlatform()
	ctx := context.Background()
	request := ports.CreateOrderRequest{
		ExternalCheckoutID: "checkout:t1",
		GroupID:            "t1",
		PaymentDueAt:       platform.OrderNow.Add(10 * time.Minute),
	}
	for _, due := range []time.Time{platform.OrderNow, platform.OrderNow.Add(11*time.Minute + time.Nanosecond)} {
		invalid := request
		invalid.PaymentDueAt = due
		_, err := platform.CreateOrder(ctx, "t1", invalid)
		var refusal *ports.OrderRefusal
		if !errors.As(err, &refusal) || refusal.Code != "MARKETPLACE_PAYMENT_DUE_INVALID" {
			t.Fatal(err)
		}
	}
	order, err := platform.CreateOrder(ctx, "t1", request)
	if err != nil {
		t.Fatal(err)
	}
	request.PaymentDueAt = time.Time{}
	repeated, err := platform.CreateOrder(ctx, "t1", request)
	if err != nil || repeated.OrderID != order.OrderID || !repeated.ExpiresAt.Equal(order.ExpiresAt) {
		t.Fatal("повтор не проверяет новый срок", repeated, err)
	}
}

func TestFakeOrderTimesUseMilliseconds(t *testing.T) {
	platform := fake.NewPlatform()
	ctx := context.Background()
	clock := fake.NewClock(platform.OrderNow.Add(345678 * time.Microsecond))
	platform.OrderClock = clock
	due := clock.Now().Add(10 * time.Minute)
	order, err := platform.CreateOrder(ctx, "t1", ports.CreateOrderRequest{
		ExternalCheckoutID: "checkout:t1",
		GroupID:            "t1",
		PaymentDueAt:       due,
	})
	if err != nil || !order.ExpiresAt.Equal(due.Truncate(time.Millisecond)) {
		t.Fatal(order, err)
	}
	clock.Advance(9 * time.Minute)
	held, err := platform.PaymentHold(ctx, "t1", order.OrderID)
	if err != nil || !held.PaymentHeldUntil.Equal(clock.Now().Add(5*time.Minute).Truncate(time.Millisecond)) {
		t.Fatal(held, err)
	}
}
