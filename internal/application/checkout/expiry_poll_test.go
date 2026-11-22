package checkout_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

func countOrderGets(e *env) *atomic.Int32 {
	calls := &atomic.Int32{}
	e.platform.CallHook = func(_ context.Context, operation string) error {
		if operation == "get_order" {
			calls.Add(1)
		}
		return nil
	}
	return calls
}

func TestOrderEventsAvoidExpiryPoll(t *testing.T) {
	for _, paid := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "paid"}[paid], func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			c := confirmedPurchase(t, e)
			calls := countOrderGets(e)
			state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
			if err != nil {
				t.Fatal(err)
			}
			for range 10 {
				e.clock.Advance(30 * time.Second)
				if err := resumeAndExecute(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			status, reason := checkout.GroupCancelled, "payment_expired"
			if paid {
				if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
					t.Fatal(err)
				}
				status, reason = checkout.GroupPaid, ""
			} else {
				e.clock.Advance(state.Groups[0].ExpiresAt.Sub(e.clock.Now()))
			}
			for _, g := range state.Groups {
				if err := applyEvent(ctx, e, g.TenantID, c.ID, checkout.SellerOrder{
					OrderID:  g.OrderID,
					Status:   status,
					Reason:   reason,
					Revision: 1,
				}); err != nil {
					t.Fatal(err)
				}
			}
			for range 5 {
				e.clock.Advance(24 * time.Hour)
				if err := resumeAndExecute(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			state, err = e.service.Get(ctx, buyerID, c.ID)
			if err != nil || calls.Load() != 0 {
				t.Fatal("обычный жизненный цикл вызвал GET", calls.Load(), err)
			}
			for _, g := range state.Groups {
				if _, found := g.NextWorkAt(); found || !g.NextPollAt.IsZero() {
					t.Fatal("после события и возврата осталась работа", g)
				}
			}
		})
	}
}

func TestLostExpiryEventChecksOnceAfterGrace(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	calls := countOrderGets(e)
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	first, second := state.Groups[0], state.Groups[1]
	if err := applyEvent(ctx, e, second.TenantID, c.ID, checkout.SellerOrder{
		OrderID:  second.OrderID,
		Status:   checkout.GroupPaid,
		Revision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	e.platform.SetOrderStatus(first.OrderID, checkout.GroupCancelled, "payment_expired")
	due := first.ExpiresAt.Add(checkout.PaymentExpiryGrace)
	e.clock.Advance(due.Sub(e.clock.Now()) - time.Nanosecond)
	for range 5 {
		if err := resumeAndExecute(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("проверка до срока с запасом")
	}
	e.clock.Advance(time.Nanosecond)
	for range 5 {
		if err := resumeAndExecute(ctx, e); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Hour)
	}
	state, err = e.service.Get(ctx, buyerID, c.ID)
	if err != nil || calls.Load() != 1 || state.Groups[0].Status != checkout.GroupCancelled {
		t.Fatal("потерянное событие требует одного GET", calls.Load(), state, err)
	}
}

func TestExpiryPollRetriesUnresolvedResult(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "awaiting", true: "unavailable"}[unavailable], func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			c := confirmedPurchase(t, e)
			calls := countOrderGets(e)
			state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
			if err != nil {
				t.Fatal(err)
			}
			if unavailable {
				e.platform.FailPoll("t1", errors.New("нет ответа"))
				e.platform.FailPoll("t2", errors.New("нет ответа"))
			}
			e.clock.Advance(state.Groups[0].ExpiresAt.Add(checkout.PaymentExpiryGrace).Sub(e.clock.Now()))
			delays := []time.Duration{
				30 * time.Second,
				time.Minute,
				2 * time.Minute,
				4 * time.Minute,
				checkout.OrderSlowPoll,
				checkout.OrderSlowPoll,
			}
			if !unavailable {
				delays = []time.Duration{
					30 * time.Second,
					30 * time.Second,
					time.Minute,
					2 * time.Minute,
					4 * time.Minute,
					8 * time.Minute,
					16 * time.Minute,
					checkout.OrderSlowPoll,
					checkout.OrderSlowPoll,
				}
			}
			for i, delay := range delays {
				if err := resumeAndExecute(ctx, e); err != nil {
					t.Fatal(err)
				}
				state, err = e.service.Get(ctx, buyerID, c.ID)
				if err != nil || calls.Load() != int32(2*(i+1)) {
					t.Fatal(calls.Load(), err)
				}
				if got := state.Groups[0].NextPollAt.Sub(e.clock.Now()); got != delay {
					t.Fatal("задержка незавершённой проверки", got, delay)
				}
				if !unavailable {
					for _, group := range state.Groups {
						if group.PollFailures != 0 || group.Status == checkout.GroupSellerUnreachable {
							t.Fatal("успешное ожидание посчитано отказом", group)
						}
					}
				}
				e.clock.Advance(delay - time.Nanosecond)
				if err := resumeAndExecute(ctx, e); err != nil || calls.Load() != int32(2*(i+1)) {
					t.Fatal("повтор раньше срока", calls.Load(), err)
				}
				e.clock.Advance(time.Nanosecond)
			}
			if !unavailable {
				e.platform.FailPoll("t1", errors.New("один отказ после успешных ответов"))
				if err := resumeAndExecute(ctx, e); err != nil {
					t.Fatal(err)
				}
				state, err = e.service.Get(ctx, buyerID, c.ID)
				if err != nil || state.Groups[0].PollFailures != 1 ||
					state.Groups[0].Status == checkout.GroupSellerUnreachable {
					t.Fatal("один отказ не означает недоступность магазина", state, err)
				}
				e.platform.FailPoll("t1", nil)
				e.clock.Advance(state.Groups[0].NextPollAt.Sub(e.clock.Now()))
				if err := resumeAndExecute(ctx, e); err != nil {
					t.Fatal(err)
				}
				state, err = e.service.Get(ctx, buyerID, c.ID)
				if err != nil || state.Groups[0].PollFailures != 0 {
					t.Fatal("успешное ожидание не сбросило отказы", state, err)
				}
			}
		})
	}
}

func TestUnknownPaymentOutcomePollsAfterDelay(t *testing.T) {
	for _, paid := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "lost_reply"}[paid], func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			c := confirmedPurchase(t, e)
			calls := countOrderGets(e)
			state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
			if err != nil {
				t.Fatal(err)
			}
			e.platform.Fail("t1", errors.New("ответ оплаты потерян"))
			if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrPaymentUnavailable) {
				t.Fatal(err)
			}
			e.platform.Fail("t1", nil)
			if paid {
				e.platform.SetOrderStatus(state.Groups[0].OrderID, checkout.GroupPaid, "")
			}
			e.clock.Advance(checkout.PaymentOutcomeDelay - time.Nanosecond)
			if err := resumeAndExecute(ctx, e); err != nil || calls.Load() != 0 {
				t.Fatal("слишком ранняя проверка оплаты", calls.Load(), err)
			}
			e.clock.Advance(time.Nanosecond)
			if err := resumeAndExecute(ctx, e); err != nil || calls.Load() != 1 {
				t.Fatal("неизвестный результат проверяется после задержки", calls.Load(), err)
			}
			if paid {
				state, err = e.service.Get(ctx, buyerID, c.ID)
				if err != nil || !state.Groups[0].Paid() || state.Groups[0].PaymentPending {
					t.Fatal("проверка восстановила потерянный ответ оплаты", state, err)
				}
			}
			if !paid {
				if _, err := e.service.Cancel(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrTransition) {
					t.Fatal("неизвестная оплата запрещает отмену", err)
				}
			}
		})
	}
}

func TestSuccessfulPaymentDoesNotPollWhileRequestRuns(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	calls := countOrderGets(e)
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.platform.AfterStubPayment = func() {
		e.platform.AfterStubPayment = nil
		for _, advance := range []time.Duration{
			0,
			time.Second,
			checkout.PaymentOutcomeDelay - time.Second - time.Nanosecond,
		} {
			e.clock.Advance(advance)
			if err := resumeAndExecute(ctx, e); err != nil || calls.Load() != 0 {
				t.Fatal("GET до ответа оплаты", calls.Load(), err)
			}
		}
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(checkout.PaymentOutcomeDelay)
	if err := resumeAndExecute(ctx, e); err != nil || calls.Load() != 0 {
		t.Fatal("успешная оплата оставила проверку", calls.Load(), err)
	}
}
