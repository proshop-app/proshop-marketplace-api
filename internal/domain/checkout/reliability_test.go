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

func TestPaymentQueueRejectsLaterGroupAndAdvances(t *testing.T) {
	for _, outcome := range []checkout.GroupStatus{
		checkout.GroupCancelled,
		checkout.GroupSellerUnreachable,
		checkout.GroupDeliveryUnavailable,
	} {
		c := ordersReady(t)
		if _, err := c.BeginOrders(contact, "https://market.example/return", t0); err != nil {
			t.Fatal(err)
		}
		if outcome == checkout.GroupCancelled {
			first := attach(t, c, 0)
			attach(t, c, 1)
			if _, err := c.Payment("t2", t0); !errors.Is(err, checkout.ErrPaymentOrder) {
				t.Fatal(err)
			}
			first.Status, first.Reason = checkout.GroupCancelled, "payment_expired"
			if _, err := c.Observe("t1", first, t0, t0); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := c.ClaimCreation("t1", "attempt", t0); err != nil {
				t.Fatal(err)
			}
			terminal := outcome
			if outcome == checkout.GroupSellerUnreachable {
				terminal = ""
			}
			if _, err := c.FailCreation(
				"t1",
				"attempt",
				"DELIVERY_UNAVAILABLE",
				"pickup_point_unavailable",
				terminal,
				nil,
				t0,
			); err != nil {
				t.Fatal(err)
			}
			attach(t, c, 1)
		}
		link, err := c.Payment("t2", t0)
		if err != nil || link.Position != 2 || c.NextPayment(t0).TenantID != "t2" {
			t.Fatalf("сдвиг очереди: %+v %v", link, err)
		}
	}
}

func TestDeliveryRecheckReturnsToDraftWithNewTotals(t *testing.T) {
	c := ordersReady(t)
	changed, err := c.RecheckDelivery(
		map[ids.TenantID]delivery.Quote{
			"t1": available(100, 0, t0.Add(time.Hour)),
			"t2": available(200, 100, t0.Add(time.Hour)),
		},
		t0,
	)
	if err != nil || !changed || c.Status() != checkout.StatusDraft || c.State().DeliveryTotal.Kopecks() != 100 {
		t.Fatalf("новые условия: %+v %v", c.State(), err)
	}
	if _, err := c.BeginOrders(contact, "https://market.example/return", t0); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal(err)
	}
}

func TestOrderAttemptClaimsAndStaleResponses(t *testing.T) {
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, "https://market.example/return", t0); err != nil {
		t.Fatal(err)
	}
	if changed, err := c.ClaimCreation("t1", "one", t0); err != nil || !changed {
		t.Fatal(err)
	}
	if changed, err := c.ClaimCreation("t1", "two", t0); err != nil || changed {
		t.Fatal("параллельная попытка")
	}
	if changed, err := c.ClaimCreation("t1", "two", t0.Add(checkout.OrderCallLease)); err != nil || !changed {
		t.Fatal(err)
	}
	if changed, err := c.FailCreation("t1", "one", "timeout", "timeout", "", nil, t0); err != nil || changed {
		t.Fatal("поздний ответ изменил новую попытку")
	}
}

func TestPollBackoffCapAndMonotoneRecovery(t *testing.T) {
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, "https://market.example/return", t0); err != nil {
		t.Fatal(err)
	}
	order := attach(t, c, 0)
	order.Status = checkout.GroupPaid
	if _, err := c.Observe("t1", order, t0, t0); err != nil {
		t.Fatal(err)
	}
	now := t0
	for i := 0; i < checkout.MaxOrderFailures+2; i++ {
		if _, err := c.ClaimPoll("t1", "token", now); err != nil {
			t.Fatal(err)
		}
		if _, err := c.FailPoll("t1", "token", "timeout", now); err != nil {
			t.Fatal(err)
		}
		next := c.Groups()[0].NextPollAt
		if next.Sub(now) > checkout.OrderSlowPoll {
			t.Fatal("нет потолка задержки")
		}
		now = next
	}
	if c.Groups()[0].Status != checkout.GroupSellerUnreachable {
		t.Fatal(c.Groups()[0])
	}
	if _, err := c.ClaimPoll("t1", "recovery", now); err != nil {
		t.Fatal(err)
	}
	regressed := order
	regressed.Status = checkout.GroupAwaitingPayment
	if _, err := c.CompletePoll("t1", "recovery", regressed, now, now); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal("ошибка опроса не разрешает регресс оплаченного заказа")
	}
	order.Status = checkout.GroupShipped
	if _, err := c.CompletePoll("t1", "recovery", order, now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if c.Groups()[0].PollFailures != 0 || c.Groups()[0].Status != checkout.GroupShipped {
		t.Fatal(c.Groups()[0])
	}
}

func TestReturnRemainingIsIsolatedFromSnapshot(t *testing.T) {
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, "https://market.example/return", t0); err != nil {
		t.Fatal(err)
	}
	order := attach(t, c, 0)
	order.Status, order.Reason = checkout.GroupCancelled, "payment_expired"
	if _, err := c.Observe("t1", order, t0, t0); err != nil {
		t.Fatal(err)
	}
	remaining := []checkout.Line{{Offer: c.Groups()[0].Lines[0].Offer, Quantity: 1, Price: money.FromKopecks(100)}}
	if _, err := c.RecordCartReturn("t1", remaining, "cart.invalid_quantity", t0); err != nil {
		t.Fatal(err)
	}
	state := c.State()
	state.Groups[0].ReturnRemaining[0].Quantity = 99
	if c.Groups()[0].ReturnRemaining[0].Quantity != 1 || c.Groups()[0].CartReturned {
		t.Fatal(c.Groups()[0])
	}
}
