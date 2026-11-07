package checkout_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

type paymentConflictUOW struct {
	ports.UnitOfWork
	remaining int
	attempts  int
}

func (u *paymentConflictUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return u.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		if u.remaining > 0 {
			u.remaining--
			u.attempts++
			return checkout.ErrConflict
		}
		return nil
	})
}

func TestSinglePaymentRecovery(t *testing.T) {
	for _, mode := range []string{"all", "partial", "failure", "commit"} {
		t.Run(mode, func(t *testing.T) {
			e := setup(t)
			c := confirmedPurchase(t, e)
			ctx := context.Background()
			state, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact)
			if err != nil {
				t.Fatal(err)
			}
			first := state.Groups[0]
			if mode == "partial" {
				e.platform.SetOrderStatus(first.OrderID, checkout.GroupPaid, "")
				if _, err := e.service.Refresh(ctx, buyerID, c.ID); err != nil {
					t.Fatal(err)
				}
				// Уже оплаченный локально магазин больше не вызывается.
				e.platform.Fail(first.TenantID, errors.New("недоступен"))
			}
			if mode == "failure" {
				e.platform.Fail(state.Groups[1].TenantID, errors.New("недоступен"))
				err := e.service.ConfirmPayment(ctx, buyerID, c.ID)
				if !errors.Is(err, checkout.ErrPaymentUnavailable) {
					t.Fatal(err)
				}
				remote, err := e.platform.GetOrder(ctx, first.TenantID, first.OrderID)
				if err != nil || remote.Status != checkout.GroupPaid {
					t.Fatal(remote, err)
				}
				local, err := e.service.Get(ctx, buyerID, c.ID)
				if err != nil || local.Status != checkout.StatusAwaitingPayment {
					t.Fatal(local, err)
				}
				e.platform.Fail(state.Groups[1].TenantID, nil)
			}
			if mode == "commit" {
				e.platform.AfterStubPayment = func() { e.store.FailCommit = errors.New("нет базы") }
				if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err == nil {
					t.Fatal("ожидался отказ")
				}
				e.store.FailCommit = nil
				e.platform.AfterStubPayment = nil
			}
			if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
				t.Fatal(err)
			}
			calls := e.platform.StubPaymentCalls()
			expectedCalls := 2
			if mode == "partial" {
				expectedCalls = 1
			}
			if mode == "failure" || mode == "commit" {
				expectedCalls = 4
			}
			if len(calls) != expectedCalls {
				t.Fatalf("попытки: %v", calls)
			}
			paid, err := e.service.Get(ctx, buyerID, c.ID)
			if err != nil || paid.Status != checkout.StatusPaid {
				t.Fatal(paid, err)
			}
			if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
				t.Fatal(err)
			}
			repeated, _ := e.service.Get(ctx, buyerID, c.ID)
			if paid.Version != repeated.Version {
				t.Fatal("повтор изменил версию")
			}
			if _, err := e.service.Pay(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrTransition) {
				t.Fatal(err)
			}
		})
	}
}

func TestSinglePaymentConcurrentUpdate(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	state, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	first := state.Groups[0]
	e.platform.AfterStubPayment = func() {
		e.platform.AfterStubPayment = nil
		err := e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			current, err := tx.Checkouts().Get(ctx, c.ID)
			if err != nil {
				return err
			}
			_, err = current.Observe(
				first.TenantID,
				checkout.SellerOrder{
					OrderID: first.OrderID,
					Status:  checkout.GroupPaid,
				},
				e.clock.Now(),
				e.clock.Now(),
			)
			if err != nil {
				return err
			}
			return tx.Checkouts().Update(ctx, current)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	state, err = e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusPaid {
		t.Fatal(state, err)
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	calls := e.platform.StubPaymentCalls()
	if len(calls) != 2 || calls[0] != "t1" || calls[1] != "t2" {
		t.Fatal(calls)
	}
}

func TestPaymentWithoutStubPreservesCart(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	before, _ := e.store.Cart(buyerID)
	service := appcheckout.NewService(e.store, e.index, e.platform, nil, nil, nil, e.clock, appcheckout.Config{})
	_, err := service.CreateOrders(context.Background(), buyerID, c.ID, orderContact)
	if !errors.Is(err, checkout.ErrPaymentUnavailable) {
		t.Fatal(err)
	}
	after, _ := e.store.Cart(buyerID)
	state, _ := e.store.Checkout(c.ID)
	if !reflect.DeepEqual(before, after) || state.CartConsumed || state.Status != checkout.StatusConfirmed {
		t.Fatal("отказ изменил корзину или покупку")
	}
	if len(e.platform.OrderRequests("t1")) != 0 {
		t.Fatal("создан заказ")
	}
}

func TestPaymentRefusalsStopAtFirstGroup(t *testing.T) {
	for _, code := range []string{
		"MARKETPLACE_STUB_PAYMENT_STATE",
		"MARKETPLACE_STUB_PAYMENT_AMOUNT",
		"ORDER_NOT_FOUND",
		"MARKETPLACE_TEMPORARY_FAILURE",
		"MARKETPLACE_DISABLED",
	} {
		t.Run(code, func(t *testing.T) {
			e := setup(t)
			c := confirmedPurchase(t, e)
			ctx := context.Background()
			if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
				t.Fatal(err)
			}
			refusal := error(&ports.OrderRefusal{Code: code})
			if code == "MARKETPLACE_DISABLED" {
				refusal = ports.ErrMarketplaceDisabled
			}
			e.platform.Fail("t1", refusal)
			want := checkout.ErrTransition
			if code == "MARKETPLACE_TEMPORARY_FAILURE" {
				want = checkout.ErrPaymentUnavailable
			}
			if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); !errors.Is(err, want) {
				t.Fatal(err)
			}
			if calls := e.platform.StubPaymentCalls(); len(calls) != 1 || calls[0] != "t1" {
				t.Fatal(calls)
			}
		})
	}
}

func TestExpiredPaymentMakesNoServerCalls(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Hour)
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrPaymentExpired) {
		t.Fatal(err)
	}
	if _, err := e.service.Payment(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrPaymentExpired) {
		t.Fatal(err)
	}
	if len(e.platform.StubPaymentCalls()) != 0 {
		t.Fatal("истёкший платёж вызвал сервер")
	}
}

func TestPaymentAfterExhaustedCreation(t *testing.T) {
	for _, refusal := range []error{
		&ports.OrderRefusal{Code: "IDEMPOTENCY_CONFLICT"},
		ports.ErrMarketplaceDisabled,
		errors.New("нет связи"),
	} {
		e := setup(t)
		c := confirmedPurchase(t, e)
		ctx := context.Background()
		e.platform.FailCreate("t1", refusal)
		state, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact)
		if err != nil {
			t.Fatal(err)
		}
		for !state.Groups[0].CreateExhausted {
			e.clock.Advance(state.Groups[0].NextCreateAt.Sub(e.clock.Now()))
			if err := e.service.Resume(ctx); err != nil {
				t.Fatal(err)
			}
			state, err = e.service.Get(ctx, buyerID, c.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
		if !state.Groups[0].CartReturned {
			t.Fatal("группа не вернулась в корзину")
		}
		if _, err := e.service.Pay(ctx, buyerID, c.ID); err != nil {
			t.Fatal(err)
		}
		if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
			t.Fatal(err)
		}
		if calls := e.platform.StubPaymentCalls(); len(calls) != 1 || calls[0] != "t2" {
			t.Fatal(calls)
		}
	}
}

func TestPaymentWithUnreachablePoll(t *testing.T) {
	for _, paid := range []bool{false, true} {
		e := setup(t)
		c := confirmedPurchase(t, e)
		ctx := context.Background()
		if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
			t.Fatal(err)
		}
		if paid {
			if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
				t.Fatal(err)
			}
		}
		e.platform.Fail("t1", errors.New("нет связи"))
		for range checkout.MaxOrderFailures {
			state, err := e.service.Refresh(ctx, buyerID, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state.Groups[0].PollFailures < checkout.MaxOrderFailures {
				e.clock.Advance(state.Groups[0].NextPollAt.Sub(e.clock.Now()))
			}
		}
		state, _ := e.service.Get(ctx, buyerID, c.ID)
		if state.Groups[0].Status != checkout.GroupSellerUnreachable {
			t.Fatal(state)
		}
		if !paid {
			err := e.service.ConfirmPayment(ctx, buyerID, c.ID)
			if !errors.Is(err, checkout.ErrPaymentUnavailable) {
				t.Fatal(err)
			}
		}
		e.platform.Fail("t1", nil)
		if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
			t.Fatal(err)
		}
		state, err := e.service.Payment(ctx, buyerID, c.ID)
		if err != nil || !checkout.Restore(state).PaymentComplete() {
			t.Fatal(state, err)
		}
	}
}

func TestSinglePaymentDoubleConfirm(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.platform.AfterStubPayment = func() {
		e.platform.AfterStubPayment = nil
		if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	state, _ := e.service.Get(ctx, buyerID, c.ID)
	if !checkout.Restore(state).PaymentComplete() {
		t.Fatal(state)
	}
}

func TestPaymentRetriesLocalConflict(t *testing.T) {
	for _, conflicts := range []int{2, 3} {
		e := setup(t)
		c := confirmedPurchase(t, e)
		ctx := context.Background()
		if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
			t.Fatal(err)
		}
		uow := &paymentConflictUOW{UnitOfWork: e.store}
		service := appcheckout.NewService(uow, e.index, e.platform, nil, nil, nil, e.clock, appcheckout.Config{
			PaymentStub: true,
		})
		e.platform.AfterStubPayment = func() { uow.remaining = conflicts }
		err := service.ConfirmPayment(ctx, buyerID, c.ID)
		if conflicts == 2 && err != nil {
			t.Fatal(err)
		}
		if conflicts == 3 && !errors.Is(err, checkout.ErrConflict) {
			t.Fatal(err)
		}
		if uow.attempts != conflicts || len(e.platform.StubPaymentCalls()) != 2 {
			t.Fatal(uow.attempts)
		}
		state, _ := e.service.Get(ctx, buyerID, c.ID)
		if checkout.Restore(state).PaymentComplete() != (conflicts == 2) {
			t.Fatal(state.Status)
		}
	}
}

func TestFakePaymentContract(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	state, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	g := state.Groups[0]
	amount, err := g.Goods.Add(g.DeliveryPrice)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status checkout.GroupStatus
		code   string
	}{
		{checkout.GroupPaid, "MARKETPLACE_STUB_PAYMENT_STATE"},
		{checkout.GroupAwaitingPayment, ""},
		{checkout.GroupShipped, ""},
		{checkout.GroupDelivered, ""},
		{checkout.GroupCancelled, "MARKETPLACE_STUB_PAYMENT_STATE"},
		{checkout.GroupReturned, "MARKETPLACE_STUB_PAYMENT_STATE"},
	} {
		e.platform.SetOrderStatus(g.OrderID, tc.status, "")
		err := e.platform.StubPayment(ctx, g.TenantID, g.OrderID, amount)
		if tc.code == "" {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		var refusal *ports.OrderRefusal
		if !errors.As(err, &refusal) || refusal.Code != tc.code {
			t.Fatal(err)
		}
	}
	for _, id := range []string{g.OrderID, "missing"} {
		err := e.platform.StubPayment(ctx, g.TenantID, id, g.Goods)
		want := "MARKETPLACE_STUB_PAYMENT_AMOUNT"
		if id == "missing" {
			want = "ORDER_NOT_FOUND"
		}
		var refusal *ports.OrderRefusal
		if !errors.As(err, &refusal) || refusal.Code != want {
			t.Fatal(err)
		}
	}
}
