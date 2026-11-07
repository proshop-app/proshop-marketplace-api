package checkout_test

import (
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

var contact = checkout.Contact{Phone: "79251234567", Name: "Покупатель"}

func ordersReady(t *testing.T) *checkout.Checkout {
	t.Helper()
	c := draft(t)
	quotes := map[ids.TenantID]delivery.Quote{
		"t1": available(100, 50, t0.Add(time.Hour)),
		"t2": available(200, 100, t0.Add(time.Hour)),
	}
	if err := c.SelectDelivery(selection, quotes, t0); err != nil {
		t.Fatal(err)
	}
	if err := c.Confirm(t0); err != nil {
		t.Fatal(err)
	}
	c.MarkSaved()
	return c
}

func attach(t *testing.T, c *checkout.Checkout, index int) checkout.SellerOrder {
	t.Helper()
	g := c.Groups()[index]
	total, err := g.Goods.Add(g.DeliveryPrice)
	if err != nil {
		t.Fatal(err)
	}
	order := checkout.SellerOrder{
		OrderID:   string(g.TenantID) + "-order",
		ExpiresAt: t0.Add(time.Hour),
		Total:     total,
		Status:    checkout.GroupAwaitingPayment,
	}
	if _, err := c.AttachOrder(g.TenantID, order, t0); err != nil {
		t.Fatal(err)
	}
	return order
}

func TestOrdersTransitions(t *testing.T) {
	c := ordersReady(t)
	changed, err := c.BeginOrders(contact, t0)
	if err != nil || !changed || c.Status() != checkout.StatusCreatingOrders {
		t.Fatalf("начало: %v", err)
	}
	c.MarkSaved()
	if changed, err := c.BeginOrders(contact, t0); err != nil || changed {
		t.Fatalf("повтор не меняет контакт: %v", err)
	}
	first, second := attach(t, c, 0), attach(t, c, 1)
	if c.Status() != checkout.StatusAwaitingPayment {
		t.Fatal(c.Status())
	}
	if changed, err := c.AttachOrder("t1", first, t0); err != nil || changed {
		t.Fatalf("повтор ответа: %v", err)
	}
	first.Status = checkout.GroupPaid
	if _, err := c.Observe("t1", first, t0.Add(time.Minute), t0); err != nil {
		t.Fatal(err)
	}
	if c.Status() != checkout.StatusPartiallyPaid {
		t.Fatal(c.State())
	}
	first.Status = checkout.GroupAwaitingPayment
	if _, err := c.Observe("t1", first, t0, t0); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal(err)
	}
	second.Status = checkout.GroupPaid
	if _, err := c.Observe("t2", second, t0, t0); err != nil {
		t.Fatal(err)
	}
	if c.Status() != checkout.StatusPaid {
		t.Fatal(c.State())
	}
}

func TestExpiryNeedsSellerCancellation(t *testing.T) {
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	first, second := attach(t, c, 0), attach(t, c, 1)
	expired := t0.Add(time.Hour)
	if _, err := c.PaymentAmount(); err != nil {
		t.Fatal(err)
	}
	if c.Status() != checkout.StatusAwaitingPayment {
		t.Fatal(c.State())
	}
	// Оплата могла пройти в последнюю секунду: истечение локальных часов не отменяет заказ.
	first.Status = checkout.GroupPaid
	if _, err := c.Observe("t1", first, expired, expired); err != nil {
		t.Fatal(err)
	}
	second.Status, second.Reason = checkout.GroupCancelled, "payment_expired"
	if _, err := c.Observe("t2", second, expired, expired); err != nil {
		t.Fatal(err)
	}
	if c.Status() != checkout.StatusPartiallyPaid {
		t.Fatal(c.Status())
	}
}

func TestAllCancelledAndSnapshotIsolation(t *testing.T) {
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	for i := range c.Groups() {
		order := attach(t, c, i)
		order.Status, order.Reason = checkout.GroupCancelled, "payment_expired"
		if _, err := c.Observe(c.Groups()[i].TenantID, order, t0, t0); err != nil {
			t.Fatal(err)
		}
	}
	if c.Status() != checkout.StatusCancelled {
		t.Fatal(c.Status())
	}
	copy := c.State()
	copy.Groups[0].Lines[0].Quantity = 99
	copy.Groups[0].Quote.Reason = "изменён"
	copy.Delivery.City = "изменён"
	if c.Groups()[0].Lines[0].Quantity == 99 || c.Groups()[0].Quote.Reason != "" || c.Delivery().City == "изменён" {
		t.Fatal("снимок не должен менять агрегат")
	}
}

func TestOrdersRequireConfirmedContactAndValidResponse(t *testing.T) {
	if _, err := draft(t).BeginOrders(contact, t0); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal(err)
	}
	c := ordersReady(t)
	_, err := c.BeginOrders(checkout.Contact{}, t0)
	if !errors.Is(err, checkout.ErrInvalidContact) {
		t.Fatal(err)
	}
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AttachOrder("t1", checkout.SellerOrder{}, t0); !errors.Is(err, checkout.ErrInvalidOrder) {
		t.Fatal(err)
	}
}

func TestGroupDeliveryThresholdAndSellerSubsidy(t *testing.T) {
	c := draft(t)
	g := c.Groups()[0]
	subsidy := participant.Subsidy{
		Enabled:   true,
		Type:      participant.SubsidyPercent,
		Value:     50,
		MaxAmount: money.FromKopecks(120),
	}
	for _, threshold := range []int64{0, 25000, 25001} {
		amount, err := g.DeliverySubsidy(money.FromKopecks(threshold), subsidy, money.FromKopecks(300), 0)
		want := int64(300)
		if threshold == 25001 {
			want = 120
		}
		if err != nil || amount.Kopecks() != want {
			t.Fatalf("порог %d: %v %v", threshold, amount, err)
		}
	}
}
