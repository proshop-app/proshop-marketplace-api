package checkout_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type refusePaymentAfterObservation struct {
	ports.PlatformAPI
	during func()
	once   sync.Once
}

// Б4 без тупика: потерянный ответ оплаты t2 (сервер платёж не получил), затем магазин t3 отменяет заказ.
// Покупатель подтверждает новый состав и повторяет оплату t2.
func TestUnknownPaymentCanBeConfirmedAfterShopCancellation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := partiallyPaidConsentPurchase(t, e)
	lost := &delayedConsentPayment{PlatformAPI: e.platform, lost: map[ids.TenantID]bool{"t2": true}}
	payer := appcheckout.NewService(
		e.store,
		lost,
		e.pickups,
		e.verifier,
		&fake.IDs{},
		e.clock,
		appcheckout.Config{
			QuoteTTL:      30 * time.Minute,
			QuoteTimeout:  time.Second,
			PaymentStub:   true,
			ReturnBaseURL: "https://market.example",
		},
	)
	firstPay := payer.ConfirmPayment(ctx, buyerID, state.ID)
	e.platform.SetOrderStatus(state.Groups[2].OrderID, checkout.GroupCancelled, "shop_cancelled")
	for range 2 {
		e.clock.Advance(time.Minute)
		if err := resumeAndExecute(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	middle, _ := e.service.Get(ctx, buyerID, state.ID)
	stored, _ := e.store.Checkout(state.ID)
	logPaymentState(t, "t2 неизвестен, t3 отменён магазином", middle, firstPay)
	t.Logf(
		"PaymentStarted=%v NeedsConfirmation=%v",
		stored.PaymentStarted,
		checkout.Restore(stored).NeedsConfirmation(),
	)
	_, cancelErr := e.service.Cancel(ctx, buyerID, state.ID)
	_, confirmErr := e.service.Confirm(ctx, buyerID, state.ID)
	retry := e.service.ConfirmPayment(ctx, buyerID, state.ID)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	final, _ := e.service.Get(ctx, buyerID, state.ID)
	logPaymentState(t, "после Confirm и повтора оплаты", final, retry)
	t.Logf(
		"Cancel при неизвестном платеже=%v; Confirm=%v; оплаты=%v; отмен t1=%d t2=%d",
		cancelErr,
		confirmErr,
		e.platform.StubPaymentCalls(),
		len(e.platform.CancellationRequests("t1")),
		len(e.platform.CancellationRequests("t2")),
	)
	if confirmErr != nil || retry != nil || final.Status != checkout.StatusPaid ||
		len(e.platform.CancellationRequests("t1")) != 0 || len(e.platform.CancellationRequests("t2")) != 0 {
		t.Fatalf("тупик: после нового согласия неизвестную оплату t2 нельзя завершить")
	}
}

// Б4 без оплаченного соседа: потерянный ответ оплаты t1 (сервер платёж не получил), магазин t2 отменяет заказ.
// Выход: новый Confirm → создание (BeginOrders) → оплата; ничего оплаченного не отменяется, нет тупика.
func TestUnknownPaymentWithoutPaidGroupsCanConfirmNewComposition(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	addConsentThirdShop(t, e)
	purchase := confirmedPurchase(t, e)
	if _, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	created, _ := e.service.Get(ctx, buyerID, purchase.ID)
	lost := &delayedConsentPayment{PlatformAPI: e.platform, lost: map[ids.TenantID]bool{"t1": true}}
	payer := appcheckout.NewService(
		e.store,
		lost,
		e.pickups,
		e.verifier,
		&fake.IDs{},
		e.clock,
		appcheckout.Config{
			QuoteTTL:      30 * time.Minute,
			QuoteTimeout:  time.Second,
			PaymentStub:   true,
			ReturnBaseURL: "https://market.example",
		},
	)
	firstPay := payer.ConfirmPayment(ctx, buyerID, purchase.ID)
	e.platform.SetOrderStatus(created.Groups[1].OrderID, checkout.GroupCancelled, "shop_cancelled")
	for range 2 {
		e.clock.Advance(time.Minute)
		if err := resumeAndExecute(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	middle, _ := e.service.Get(ctx, buyerID, purchase.ID)
	stored, _ := e.store.Checkout(purchase.ID)
	logPaymentState(t, "t1 неизвестен, t2 отменён магазином", middle, firstPay)
	t.Logf(
		"PaymentStarted=%v NeedsConfirmation=%v",
		stored.PaymentStarted,
		checkout.Restore(stored).NeedsConfirmation(),
	)
	_, cancelErr := e.service.Cancel(ctx, buyerID, purchase.ID)
	if !errors.Is(cancelErr, checkout.ErrTransition) {
		t.Fatal("неизвестный исход платежа должен запрещать отмену", cancelErr)
	}
	confirmed, confirmErr := e.service.Confirm(ctx, buyerID, purchase.ID)
	_, cancelErr = e.service.Cancel(ctx, buyerID, purchase.ID)
	if !errors.Is(cancelErr, checkout.ErrTransition) {
		t.Fatal("новое согласие не снимает защиту неизвестного платежа", cancelErr)
	}
	logPaymentState(t, "Confirm", confirmed, confirmErr)
	ordered, orderErr := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	logPaymentState(t, "CreateOrders после Confirm", ordered, orderErr)
	retry := e.service.ConfirmPayment(ctx, buyerID, purchase.ID)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	final, _ := e.service.Get(ctx, buyerID, purchase.ID)
	logPaymentState(t, "итог", final, retry)
	t.Logf(
		"оплаты=%v; создано заказов t1=%d t3=%d; отмен t1=%d t3=%d",
		e.platform.StubPaymentCalls(),

		len(e.platform.OrderRequests("t1")),
		len(e.platform.OrderRequests("t3")),

		len(e.platform.CancellationRequests("t1")),
		len(e.platform.CancellationRequests("t3")),
	)
	if confirmErr != nil || orderErr != nil || retry != nil || final.Status != checkout.StatusPaid ||
		len(e.platform.OrderRequests("t1")) != 1 || len(e.platform.OrderRequests("t3")) != 1 {
		t.Fatalf("тупик или повторное создание после нового согласия при неизвестной оплате")
	}
}

func (p *refusePaymentAfterObservation) StubPayment(
	ctx context.Context,
	tenant ids.TenantID,
	id string,
	amount money.Money,
) error {
	p.once.Do(p.during)
	return &ports.OrderRefusal{Code: "ORDER_NOT_FOUND"}
}

// Вариант с доказанным отсутствием платежа: отмена t2 магазином наблюдается во время оплаты t1,
// затем сервер отвечает ORDER_NOT_FOUND (платежа нет). Может ли покупатель подтвердить состав и оплатить.
func TestRefusedPaymentWithoutPaidGroupsCanConfirmNewComposition(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	addConsentThirdShop(t, e)
	purchase := confirmedPurchase(t, e)
	if _, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	created, _ := e.service.Get(ctx, buyerID, purchase.ID)
	upstream := &refusePaymentAfterObservation{PlatformAPI: e.platform}
	upstream.during = func() {
		e.platform.SetOrderStatus(created.Groups[1].OrderID, checkout.GroupCancelled, "shop_cancelled")
		e.clock.Advance(time.Minute)
		_ = resumeAndExecute(ctx, e)
	}
	payer := appcheckout.NewService(
		e.store,
		upstream,
		e.pickups,
		e.verifier,
		&fake.IDs{},
		e.clock,
		appcheckout.Config{
			QuoteTTL:      30 * time.Minute,
			QuoteTimeout:  time.Second,
			PaymentStub:   true,
			ReturnBaseURL: "https://market.example",
		},
	)
	payErr := payer.ConfirmPayment(ctx, buyerID, purchase.ID)
	stored, _ := e.store.Checkout(purchase.ID)
	middle, _ := e.service.Get(ctx, buyerID, purchase.ID)
	logPaymentState(t, "после отказа оплаты t1 и отмены t2", middle, payErr)
	t.Logf(
		"PaymentStarted=%v NeedsConfirmation=%v",
		stored.PaymentStarted,
		checkout.Restore(stored).NeedsConfirmation(),
	)
	_, confirmErr := e.service.Confirm(ctx, buyerID, purchase.ID)
	retry := e.service.ConfirmPayment(ctx, buyerID, purchase.ID)
	t.Logf("Confirm=%v; повтор оплаты=%v; оплаты=%v", confirmErr, retry, e.platform.StubPaymentCalls())
	if confirmErr != nil || retry != nil {
		t.Fatalf("новое согласие недоступно: покупка без оплаченных групп после изменения состава во время оплаты")
	}
}
