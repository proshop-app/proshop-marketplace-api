package checkout_test

import (
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

func creatingWithDeadline(t *testing.T) *checkout.Checkout {
	t.Helper()
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	if err := c.SetCreationDeadline(t0.Add(15*time.Second), t0); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCreationDeadlineAndLateResult(t *testing.T) {
	c := creatingWithDeadline(t)
	if _, err := c.ClaimCreation("t1", "attempt", t0.Add(5*time.Second), t0); err != nil {
		t.Fatal(err)
	}
	if err := c.BeginPayment(t0); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal(err)
	}
	if changed, err := c.ExpireCreation(t0.Add(14900 * time.Millisecond)); err != nil || changed {
		t.Fatal(changed, err)
	}
	if changed, err := c.ExpireCreation(t0.Add(15 * time.Second)); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if !c.CreationComplete() || !c.Groups()[0].ReconciliationRequired || c.Groups()[1].ReconciliationRequired {
		t.Fatal(c.State())
	}
	if changed, err := c.ExpireCreation(t0.Add(time.Minute)); err != nil || changed {
		t.Fatal(changed, err)
	}
	order := attach(t, c, 0)
	if c.Groups()[0].Status != checkout.GroupUnconfirmed || !c.Groups()[0].ReconciliationRequired {
		t.Fatal("поздний заказ не открывает оплату", c.State())
	}
	order.Status, order.Reason = checkout.GroupCancelled, "expired"
	if _, err := c.Observe("t1", order, t0, t0); err != nil {
		t.Fatal(err)
	}
	if c.Groups()[0].ReconciliationRequired {
		t.Fatal(c.State())
	}
}

func TestBuyerCancellationAndCreationResult(t *testing.T) {
	c := creatingWithDeadline(t)
	order := attach(t, c, 0)
	if _, err := c.Cancel(t0); err != nil {
		t.Fatal(err)
	}
	if c.Groups()[0].Status != checkout.GroupCancellationRequested ||
		c.Groups()[0].CreationResult() != checkout.CreationCreated || !c.Groups()[0].Returnable() {
		t.Fatal(c.State())
	}
	if changed, err := c.Cancel(t0); err != nil || changed {
		t.Fatal(changed, err)
	}
	if err := c.BeginPayment(t0); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal(err)
	}
	order.Status, order.Reason = checkout.GroupCancelled, "expired"
	if _, err := c.Observe("t1", order, t0, t0); err != nil {
		t.Fatal(err)
	}
	if c.Groups()[0].Status != checkout.GroupCancelled ||
		c.Groups()[0].CreationResult() != checkout.CreationCreated || c.Groups()[0].ReconciliationRequired {
		t.Fatal(c.State())
	}
}

func TestAbandonedFailureAndPaymentFence(t *testing.T) {
	c := creatingWithDeadline(t)
	if _, err := c.ClaimCreation("t1", "attempt", t0.Add(5*time.Second), t0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Cancel(t0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.FailCreation(
		"t1",
		"attempt",
		"refused",
		"отказ",
		checkout.GroupRejected,
		false,
		nil,
		t0,
	); err != nil {
		t.Fatal(err)
	}
	if c.Groups()[0].ReconciliationRequired {
		t.Fatal(c.State())
	}
	c = creatingWithDeadline(t)
	attach(t, c, 0)
	attach(t, c, 1)
	if err := c.ClaimPayment("one", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Cancel(t0); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal(err)
	}
	if err := c.ClaimPayment("two", t0); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishPayment("one", true, t0); err != nil {
		t.Fatal(err)
	}
	if !c.State().PaymentStarted {
		t.Fatal("старый отказ снял блокировку новой оплаты")
	}
	if err := c.FinishPayment("two", true, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Cancel(t0); err != nil {
		t.Fatal(err)
	}
}

func TestCancelWithoutAttemptIsFinal(t *testing.T) {
	c := creatingWithDeadline(t)
	if _, err := c.Cancel(t0); err != nil {
		t.Fatal(err)
	}
	for _, g := range c.Groups() {
		if g.Status != checkout.GroupRejected || g.ReconciliationRequired ||
			g.CreationResult() != checkout.CreationRejected {
			t.Fatal(g)
		}
	}
	if !c.CreationComplete() {
		t.Fatal(c.State())
	}
}

func TestDeadlineWaitsForActiveAttempt(t *testing.T) {
	c := creatingWithDeadline(t)
	deadline := t0.Add(15 * time.Second)
	lease := deadline.Add(100 * time.Millisecond)
	if _, err := c.ClaimCreation("t1", "attempt", lease, deadline.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExpireCreation(deadline); err != nil {
		t.Fatal(err)
	}
	if c.Groups()[0].Abandoned || c.Groups()[1].Status != checkout.GroupUnconfirmed {
		t.Fatal(c.State())
	}
	if _, err := c.ExpireCreation(lease); err != nil {
		t.Fatal(err)
	}
	if c.Groups()[0].Status != checkout.GroupUnconfirmed {
		t.Fatal(c.State())
	}
}

func TestNextWorkAtMatchesDueRules(t *testing.T) {
	for _, g := range []checkout.Group{
		{},
		{Status: checkout.GroupCreating},
		{Status: checkout.GroupSellerUnreachable, CreateExhausted: true},
		{Status: checkout.GroupSellerUnreachable, CreateExhausted: true, NextReturnAt: t0.Add(time.Minute)},
		{Status: checkout.GroupCancelled, CartReturned: true},
		{Status: checkout.GroupAwaitingPayment, OrderID: "order", NextPollAt: t0},
		{Status: checkout.GroupUnconfirmed, Abandoned: true, ReconciliationRequired: true},
		{
			Status:                 checkout.GroupUnconfirmed,
			Abandoned:              true,
			ReconciliationRequired: true,
			OrderID:                "order",
			NextPollAt:             t0.Add(time.Hour),
			NextReturnAt:           t0.Add(time.Minute),
		},
		{
			Status:          checkout.GroupSellerUnreachable,
			LastOrderStatus: checkout.GroupCancelled,
			OrderID:         "order",
			NextPollAt:      t0,
			NextReturnAt:    t0.Add(time.Minute),
		},
	} {
		next, found := g.NextWorkAt()
		for _, now := range []time.Time{{}, t0.Add(-time.Second), t0, t0.Add(time.Minute), t0.Add(time.Hour)} {
			scheduled := found && !now.Before(next)
			if scheduled != (g.PollDue(now) || g.ReturnDue(now)) {
				t.Fatalf("%+v: %v, %v at %v", g, next, found, now)
			}
		}
	}
}
