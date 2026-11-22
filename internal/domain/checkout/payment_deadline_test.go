package checkout_test

import (
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

func TestCreationRejectsDifferentPaymentDeadline(t *testing.T) {
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	group := c.Groups()[0]
	total, err := group.Goods.Add(group.DeliveryPrice)
	if err != nil {
		t.Fatal(err)
	}
	order := checkout.SellerOrder{
		OrderID:         "o1",
		Total:           total,
		ExpiresAt:       group.ExpiresAt.Add(time.Second),
		Carrier:         c.Delivery().Carrier,
		PickupPointCode: c.Delivery().PickupPointCode,
	}
	if _, err := c.AttachOrder(group.TenantID, order, t0); !errors.Is(err, checkout.ErrInvalidOrder) {
		t.Fatal(err)
	}
}

func TestHoldStateFollowsRevisionAndChangesExpiryWork(t *testing.T) {
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	order := attach(t, c, 0)
	order.Revision = 3
	order.PaymentHeldUntil = t0.Add(15 * time.Minute)
	if changed, err := c.ApplyOrderState("t1", order, t0); err != nil || !changed {
		t.Fatal(changed, err)
	}
	deadline := c.Groups()[0].PaymentDeadline()
	order.Revision = 2
	order.PaymentHeldUntil = t0.Add(20 * time.Minute)
	if changed, err := c.ApplyOrderState("t1", order, t0); err != nil || changed {
		t.Fatal("старая ревизия изменила удержание", changed, err)
	}
	group := c.Groups()[0]
	if !group.PaymentDeadline().Equal(deadline) || group.PollDue(deadline) ||
		!group.PollDue(deadline.Add(checkout.PaymentExpiryGrace)) {
		t.Fatal(group)
	}
}
