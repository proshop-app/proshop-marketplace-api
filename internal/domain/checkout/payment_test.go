package checkout_test

import (
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
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
			if _, err := c.Observe("t1", first, t0); err != nil {
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
		if _, err := c.Observe("t1", first, t0); !errors.Is(err, checkout.ErrTransition) {
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
	if _, err := c.Observe("t2", second, t0); err != nil {
		t.Fatal(err)
	}
	if err := c.Confirm(t0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	if err := c.ClaimPayment("payment", c.PaymentOrders(), t0); err != nil {
		t.Fatal(err)
	}
	now := t0.Add(checkout.PaymentOutcomeDelay)
	if claimed, err := c.ClaimPoll("t1", "old-poll", now); err != nil || !claimed {
		t.Fatal(err)
	}
	if _, err := c.Pay(now); err != nil {
		t.Fatal(err)
	}
	if changed, err := c.CompletePoll("t1", "old-poll", first, now); err != nil || changed {
		t.Fatal("поздний ответ изменил оплату", err)
	}
	if c.Groups()[0].Status != checkout.GroupPaid || c.Groups()[1].Status != checkout.GroupCancelled {
		t.Fatal(c.State())
	}
	if changed, err := c.Pay(now); err != nil || changed {
		t.Fatal("повтор с отменённой группой", err)
	}
}

func TestPartialPaymentCompositionNeedsConfirmationAndPreservesPaidOrder(t *testing.T) {
	lines := []checkout.Line{}
	quotes := map[ids.TenantID]delivery.Quote{}
	for _, tenant := range []ids.TenantID{"t1", "t2", "t3"} {
		lines = append(lines, checkout.Line{
			Offer:    ids.OfferID{Tenant: tenant, Product: "p1"},
			Name:     "Товар",
			Quantity: 1,
			Price:    money.FromKopecks(100),
		})
		quotes[tenant] = available(200, t0.Add(time.Hour))
	}
	c, err := checkout.New(id, "7a1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b", "partial-three-shops", lines, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SelectDelivery(selection, quotes, t0); err != nil {
		t.Fatal(err)
	}
	if err := c.Confirm(t0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	first := attach(t, c, 0)
	attach(t, c, 1)
	attach(t, c, 2)
	first.Status = checkout.GroupPaid
	if _, err := c.Observe("t1", first, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RemoveGroup("t2", t0); err != nil {
		t.Fatal(err)
	}
	if c.Status() != checkout.StatusPartiallyPaid || !c.NeedsConfirmation() || !c.Groups()[0].Paid() {
		t.Fatal(c.State())
	}
	if _, err := c.PaymentAmount(); !errors.Is(err, checkout.ErrDeliveryChanged) {
		t.Fatal(err)
	}
	if err := c.Confirm(t0); err != nil {
		t.Fatal(err)
	}
	amount, err := c.PaymentAmount()
	if err != nil || amount.Kopecks() != 100 || c.NeedsConfirmation() {
		t.Fatal(c.State(), err)
	}
	if _, err := c.Cancel(t0); err != nil {
		t.Fatal(err)
	}
	if c.Status() != checkout.StatusPaid || c.State().Total.Kopecks() != 100 || c.Groups()[0].CancellationRequired() {
		t.Fatal(c.State())
	}
}
