package checkout_test

import (
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

func TestWholePaymentAndRepeat(t *testing.T) {
	for _, partial := range []bool{false, true} {
		c := ordersReady(t)
		if _, err := c.BeginOrders(contact, t0); err != nil {
			t.Fatal(err)
		}
		first := attach(t, c, 0)
		attach(t, c, 1)
		if partial {
			first.Status = checkout.GroupPaid
			if _, err := c.Observe("t1", first, t0, t0); err != nil {
				t.Fatal(err)
			}
		}
		amount, err := c.PaymentAmount()
		want := c.State().Total
		if partial {
			want, _ = c.Groups()[1].Goods.Add(c.Groups()[1].DeliveryPrice)
		}
		if err != nil || amount != want {
			t.Fatalf("сумма: %v, %v", amount, err)
		}
		if changed, err := c.Pay(t0); err != nil || !changed || c.Status() != checkout.StatusPaid {
			t.Fatal(c.State(), err)
		}
		c.MarkSaved()
		version := c.State().Version
		if changed, err := c.Pay(t0.Add(time.Minute)); err != nil || changed || c.State().Version != version {
			t.Fatal("повтор изменил покупку", err)
		}
		first.Status = checkout.GroupAwaitingPayment
		if _, err := c.Observe("t1", first, t0, t0); !errors.Is(err, checkout.ErrTransition) {
			t.Fatal("опрос откатил оплату", err)
		}
	}
}

func TestPaymentInvalidatesInFlightPoll(t *testing.T) {
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	first := attach(t, c, 0)
	second := attach(t, c, 1)
	second.Status, second.Reason = checkout.GroupCancelled, "cancelled"
	if _, err := c.Observe("t2", second, t0, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClaimPoll("t1", "old-poll", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Pay(t0); err != nil {
		t.Fatal(err)
	}
	if changed, err := c.CompletePoll("t1", "old-poll", first, t0, t0); err != nil || changed {
		t.Fatal("поздний ответ изменил оплату", err)
	}
	if c.Groups()[0].Status != checkout.GroupPaid || c.Groups()[1].Status != checkout.GroupCancelled {
		t.Fatal(c.State())
	}
	if changed, err := c.Pay(t0); err != nil || changed {
		t.Fatal("повтор с отменённой группой", err)
	}
}
