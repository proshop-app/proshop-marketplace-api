package checkout_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

func TestPaymentHoldWindowAndRepeat(t *testing.T) {
	for _, delay := range []time.Duration{4 * time.Minute, 6 * time.Minute} {
		t.Run(delay.String(), func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			c := confirmedPurchase(t, e)
			intent := e.clock.Now()
			state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
			if err != nil {
				t.Fatal(err)
			}
			for _, group := range state.Groups {
				requests := e.platform.OrderRequests(group.TenantID)
				if !group.ExpiresAt.Equal(intent.Add(checkout.PaymentWindow)) ||
					!requests[0].PaymentDueAt.Equal(group.ExpiresAt) {
					t.Fatal("единый срок намерения потерян", group, requests)
				}
			}
			var holds, gets atomic.Int32
			e.platform.CallHook = func(_ context.Context, operation string) error {
				if operation == "payment_hold" {
					holds.Add(1)
				}
				if operation == "get_order" {
					gets.Add(1)
				}
				return nil
			}
			due := state.Groups[0].ExpiresAt
			e.clock.Advance(due.Sub(e.clock.Now()) - time.Second)
			held, err := e.service.Pay(ctx, buyerID, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !held.Groups[0].PaymentHeldUntil.Equal(due.Add(5*time.Minute - time.Second)) {
				t.Fatal("позднее удержание должно дать 5 минут от команды", held)
			}
			e.clock.Advance(time.Second)
			repeated, err := e.service.Pay(ctx, buyerID, c.ID)
			if err != nil || holds.Load() != 2 || gets.Load() != 0 ||
				!repeated.Groups[0].PaymentHeldUntil.Equal(held.Groups[0].PaymentHeldUntil) {
				t.Fatal("повтор не должен вызывать магазин или двигать срок", repeated, holds.Load(), err)
			}
			e.clock.Advance(delay)
			err = e.service.ConfirmPayment(ctx, buyerID, c.ID)
			if delay == 4*time.Minute && err != nil {
				t.Fatal(err)
			}
			if delay == 6*time.Minute && !errors.Is(err, checkout.ErrPaymentExpired) {
				t.Fatal("оплата после удержания запрещена", err)
			}
		})
	}
}

func TestCreateHoldAndPayWithMicrosecondClock(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.clock.Advance(345678 * time.Microsecond)
	purchase := confirmedPurchase(t, e)
	due := e.clock.Now().Add(checkout.PaymentWindow).Truncate(time.Millisecond)
	created, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	if err != nil || created.Status != checkout.StatusAwaitingPayment {
		t.Fatal(created.Status, err)
	}
	for _, group := range created.Groups {
		if group.OrderID == "" || !group.ExpiresAt.Equal(due) {
			t.Fatal(group)
		}
	}

	e.clock.Advance(checkout.PaymentWindow - time.Second)
	holdNow := e.clock.Now()
	held, err := e.service.Pay(ctx, buyerID, purchase.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range held.Groups {
		if !group.PaymentHeldUntil.Equal(holdNow.Add(5 * time.Minute).Truncate(time.Millisecond)) {
			t.Fatal("удержание магазина должно иметь точность до миллисекунд", group)
		}
	}
	e.clock.Advance(time.Second)
	if err := e.service.ConfirmPayment(ctx, buyerID, purchase.ID); err != nil {
		t.Fatal(err)
	}
	paid, err := e.service.Get(ctx, buyerID, purchase.ID)
	if err != nil || paid.Status != checkout.StatusPaid || len(e.platform.StubPaymentCalls()) != 2 {
		t.Fatal(paid.Status, err)
	}
}

func TestInvalidShopPaymentDueRejectsCreationAndReturnsItems(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.carts.Remove(ctx, buyerID, p2); err != nil {
		t.Fatal(err)
	}
	purchase := confirmedPurchase(t, e)
	e.platform.FailCreate("t1", &ports.OrderRefusal{Code: "MARKETPLACE_PAYMENT_DUE_INVALID"})
	state, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(checkout.CreationRetryDelay)
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	group := state.Groups[0]
	if group.Status != checkout.GroupRejected || group.LastErrorCode != "MARKETPLACE_PAYMENT_DUE_INVALID" ||
		!group.CartReturned || len(e.platform.OrderRequests("t1")) != 1 || e.platform.OrderCount() != 0 {
		t.Fatal("срок отклонён окончательно, товар должен вернуться без повтора создания", group)
	}
	basket, err := e.carts.Get(ctx, buyerID)
	if err != nil || len(basket.Lines) != 1 || basket.Lines[0].Offer != p1 || basket.Lines[0].Quantity != 2 {
		t.Fatal(basket, err)
	}
}

func TestPaymentHoldPartialFailureAndParallelRetry(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	e.platform.CallHook = func(ctx context.Context, operation string) error {
		if operation != "payment_hold" {
			return nil
		}
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e.platform.Fail("t2", errors.New("недоступен"))
	result := make(chan error, 1)
	go func() {
		_, err := e.service.Pay(ctx, buyerID, c.ID)
		result <- err
	}()
	for range 2 {
		<-started
	}
	close(release)
	if err := <-result; !errors.Is(err, checkout.ErrPaymentUnavailable) {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].PaymentHeldUntil.IsZero() || !state.Groups[1].PaymentHeldUntil.IsZero() {
		t.Fatal("частичный успех потерян", state, err)
	}
	e.platform.Fail("t2", nil)
	if _, err := e.service.Pay(ctx, buyerID, c.ID); err != nil || calls.Load() != 3 {
		t.Fatal("повтор должен вызвать только t2", calls.Load(), err)
	}
}

func TestPaymentHoldExpiryPollAndNoHold(t *testing.T) {
	for _, hold := range []bool{false, true} {
		e := setup(t)
		ctx := context.Background()
		c := confirmedPurchase(t, e)
		state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
		if err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(checkout.PaymentWindow - time.Second)
		if hold {
			state, err = e.service.Pay(ctx, buyerID, c.ID)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			e.clock.Advance(time.Second)
			if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrPaymentExpired) {
				t.Fatal(err)
			}
		}
		calls := countOrderGets(e)
		due := state.Groups[0].PaymentDeadline().Add(checkout.PaymentExpiryGrace)
		e.clock.Advance(due.Sub(e.clock.Now()) - time.Nanosecond)
		if err := resumeAndExecute(ctx, e); err != nil || calls.Load() != 0 {
			t.Fatal("ранняя страховочная проверка", calls.Load(), err)
		}
		e.clock.Advance(time.Nanosecond)
		if err := resumeAndExecute(ctx, e); err != nil || calls.Load() != 2 {
			t.Fatal("нет проверки после действующего срока", calls.Load(), err)
		}
	}
}

func TestCreationRetryKeepsPaymentDue(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	e.platform.FailCreate("t1", &ports.OrderRefusal{Code: "MARKETPLACE_TEMPORARY_FAILURE"})
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.platform.FailCreate("t1", nil)
	e.clock.Advance(time.Second)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	requests := e.platform.OrderRequests("t1")
	if len(requests) < 2 || !requests[0].PaymentDueAt.Equal(requests[1].PaymentDueAt) ||
		!requests[0].PaymentDueAt.Equal(e.platform.OrderRequests("t2")[0].PaymentDueAt) {
		t.Fatal("повтор изменил общий срок", requests)
	}
}

func TestPaymentHoldExpiredRefusalKeepsOtherHold(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.platform.Fail("t2", &ports.OrderRefusal{Code: "ORDER_PAYMENT_EXPIRED"})
	state, err := e.service.Pay(ctx, buyerID, c.ID)
	if !errors.Is(err, checkout.ErrPaymentExpired) || state.Groups[0].PaymentHeldUntil.IsZero() ||
		!state.Groups[1].PaymentHeldUntil.IsZero() || len(e.platform.StubPaymentCalls()) != 0 {
		t.Fatal("истечение закрывает кассу, сохраняя успешное удержание", state, err)
	}
}

func TestEarlyPaymentHoldKeepsOriginalDeadline(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	due := state.Groups[0].ExpiresAt
	e.clock.Advance(time.Minute)
	// Ограждение само ограничивает вызов: приложение не добавляет конкурирующий срок.
	e.platform.CallHook = func(ctx context.Context, operation string) error {
		if operation == "payment_hold" {
			if _, found := ctx.Deadline(); found {
				return errors.New("приложение добавило срок удержанию")
			}
		}
		return nil
	}
	held, err := e.service.Pay(ctx, buyerID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range held.Groups {
		if !group.PaymentHeldUntil.Equal(due) {
			t.Fatal("раннее удержание сократило срок", group)
		}
	}
	e.clock.Advance(8 * time.Minute)
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
		t.Fatal("оплата на 9-й минуте должна пройти", err)
	}
}

func TestStubPaymentExpiredRefusal(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	// Часы магазина пересекли срок уже после локальной проверки готовности.
	e.platform.CallHook = func(_ context.Context, operation string) error {
		if operation == "stub_payment" {
			return &ports.OrderRefusal{Code: "ORDER_PAYMENT_EXPIRED"}
		}
		return nil
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrPaymentExpired) {
		t.Fatal("отказ магазина должен стать истечением срока оплаты", err)
	}
}
