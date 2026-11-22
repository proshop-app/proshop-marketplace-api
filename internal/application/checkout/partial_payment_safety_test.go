package checkout_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type removeDuringPayment struct {
	ports.PlatformAPI
	remove func() error
	err    error
	done   bool
}

type latePaymentCommit struct {
	ports.PlatformAPI
	tenant  ids.TenantID
	pending func()
}

func paymentCartContents(t *testing.T, e *env) string {
	t.Helper()
	basket, err := e.carts.Get(context.Background(), buyerID)
	if err != nil {
		return "нет корзины"
	}
	out := ""
	for _, line := range basket.Lines {
		out += line.Offer.String() + "×" + string(rune('0'+line.Quantity)) + " "
	}
	return out
}

// Н1, стало: недоступный магазин убирается явной командой, здоровая группа оплачивается.
func TestRemovedUnreachableShopLeavesHealthyGroupPayable(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	e.platform.FailCreate("t1", errors.New("timeout"))
	if _, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(appcheckout.CreationTimeout + time.Second)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	e.platform.Fail("t1", errors.New("timeout"))
	removed, err := e.service.RemoveGroup(ctx, buyerID, purchase.ID, "t1")
	logPaymentState(t, "RemoveGroup t1", removed, err)
	again, againErr := e.service.RemoveGroup(ctx, buyerID, purchase.ID, "t1")
	logPaymentState(t, "повтор RemoveGroup t1", again, againErr)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	t.Logf(
		"корзина после удаления: %s; cancel-by-checkout t1: %d",
		paymentCartContents(
			t,
			e,
		),
		len(e.platform.CancellationRequests("t1")),
	)
	confirmed, confirmErr := e.service.Confirm(ctx, buyerID, purchase.ID)
	logPaymentState(t, "Confirm", confirmed, confirmErr)
	ordered, ordersErr := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	logPaymentState(t, "CreateOrders", ordered, ordersErr)
	payErr := e.service.ConfirmPayment(ctx, buyerID, purchase.ID)
	t.Logf(
		"ConfirmPayment=%v; оплаты: %v; созданий t2: %d",
		payErr,
		e.platform.StubPaymentCalls(),
		len(e.platform.OrderRequests("t2")),
	)
	if payErr != nil || len(e.platform.StubPaymentCalls()) != 1 {
		t.Fatalf("здоровая группа не оплачена после удаления недоступного магазина")
	}
}

// Частичная оплата: покупатель убирает неоплаченную группу — что с оплаченной.
func TestRemovingUnpaidGroupPreservesPaidShipmentAndAmount(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	state, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	e.platform.SetOrderStatus(state.Groups[0].OrderID, checkout.GroupPaid, "")
	if err := announceOrder(ctx, e, state.Groups[0].TenantID, state.ID, state.Groups[0].OrderID); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	removed, removeErr := e.service.RemoveGroup(ctx, buyerID, purchase.ID, "t2")
	logPaymentState(t, "RemoveGroup t2 при оплаченном t1", removed, removeErr)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	e.platform.SetOrderStatus(state.Groups[0].OrderID, checkout.GroupShipped, "")
	if err := announceOrder(ctx, e, state.Groups[0].TenantID, state.ID, state.Groups[0].OrderID); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(15 * time.Minute)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	shipped, _ := e.service.Get(ctx, buyerID, purchase.ID)
	logPaymentState(t, "оплаченный t1 отправлен", shipped, nil)
	cancelled, cancelErr := e.service.Cancel(ctx, buyerID, purchase.ID)
	logPaymentState(t, "Cancel покупки", cancelled, cancelErr)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	t.Logf("cancel-by-checkout t1 (оплачен): %d", len(e.platform.CancellationRequests("t1")))
	e.clock.Advance(15 * time.Minute)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	final, _ := e.service.Get(ctx, buyerID, purchase.ID)
	logPaymentState(t, "итог после сверки отказа магазина", final, nil)
	if shipped.Status == checkout.StatusDraft || len(e.platform.CancellationRequests("t1")) > 0 {
		t.Fatalf("покупка с оплаченной отправленной группой стала черновиком/получила отмену оплаченного заказа")
	}
}

func (p *removeDuringPayment) StubPayment(
	ctx context.Context, tenant ids.TenantID, id string, amount money.Money,
) error {
	if !p.done {
		p.done = true
		p.err = p.remove()
	}
	return p.PlatformAPI.StubPayment(ctx, tenant, id, amount)
}

// Гонка: покупатель убирает группу, пока касса уже проводит платёж.
func TestGroupRemovalWaitsForActivePayment(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	if _, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	upstream := &removeDuringPayment{PlatformAPI: e.platform}
	svc := appcheckout.NewService(
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
	upstream.remove = func() error {
		_, err := svc.RemoveGroup(ctx, buyerID, purchase.ID, "t2")
		return err
	}
	payErr := svc.ConfirmPayment(ctx, buyerID, purchase.ID)
	after, _ := svc.Get(ctx, buyerID, purchase.ID)
	logPaymentState(t, "после ConfirmPayment с удалением во время платежа", after, payErr)
	t.Logf(
		"RemoveGroup во время платежа: %v; оплаты: %v",
		upstream.err,
		e.platform.StubPaymentCalls(),
	)
	if upstream.err == nil {
		t.Fatalf("удаление группы прошло во время начатого платежа")
	}
}

// Частичная оплата: неоплаченный t2 окончательно не готов → ReviewGroups при Pay; затем отмена покупки.
func TestReadinessReviewAndCancellationPreservePaidGroup(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	state, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	e.platform.SetOrderStatus(state.Groups[0].OrderID, checkout.GroupPaid, "")
	if err := announceOrder(ctx, e, state.Groups[0].TenantID, state.ID, state.Groups[0].OrderID); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	seller, _ := e.store.Participant("t2")
	seller.Stage = "disconnected"
	e.store.SeedParticipant(seller)
	reviewed, payErr := e.service.Pay(ctx, buyerID, purchase.ID)
	logPaymentState(t, "Pay при частичной оплате и неготовом t2", reviewed, payErr)
	cancelled, cancelErr := e.service.Cancel(ctx, buyerID, purchase.ID)
	logPaymentState(t, "Cancel", cancelled, cancelErr)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	t.Logf("cancel-by-checkout оплаченного t1: %d", len(e.platform.CancellationRequests("t1")))
	e.clock.Advance(15 * time.Minute)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	final, _ := e.service.Get(ctx, buyerID, purchase.ID)
	logPaymentState(t, "итог после сверки отказа магазина", final, nil)
	if cancelErr == nil && len(e.platform.CancellationRequests("t1")) > 0 {
		t.Fatalf("отмена покупки после пересмотра частично оплаченной покупки отправила отмену оплаченного заказа")
	}
}

// StubPayment: клиент площадки получил таймаут, а сервер магазина фиксирует оплату позже.
func (p *latePaymentCommit) StubPayment(ctx context.Context, tenant ids.TenantID, id string, amount money.Money) error {
	if tenant == p.tenant && p.pending == nil {
		p.pending = func() { _ = p.PlatformAPI.StubPayment(context.Background(), tenant, id, amount) }
		return errors.New("context deadline exceeded")
	}
	return p.PlatformAPI.StubPayment(ctx, tenant, id, amount)
}

// Предусловие находки «частичная оплата → черновик» в штатном коде: потерянный ответ оплаты t1.
func TestLatePaymentReplyCannotCauseCancellationOfPaidOrder(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	if _, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	upstream := &latePaymentCommit{PlatformAPI: e.platform, tenant: "t1"}
	svc := appcheckout.NewService(
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
	payErr := svc.ConfirmPayment(ctx, buyerID, purchase.ID)
	upstream.pending()
	e.clock.Advance(time.Minute)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	current, _ := e.store.Checkout(purchase.ID)
	after, _ := e.service.Get(ctx, buyerID, purchase.ID)
	logPaymentState(t, "после потерянного ответа оплаты t1", after, payErr)
	t.Logf("PaymentStarted=%v", current.PaymentStarted)
	removed, removeErr := e.service.RemoveGroup(ctx, buyerID, purchase.ID, "t2")
	logPaymentState(t, "RemoveGroup t2", removed, removeErr)
	cancelled, cancelErr := e.service.Cancel(ctx, buyerID, purchase.ID)
	logPaymentState(t, "Cancel", cancelled, cancelErr)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	t.Logf("cancel-by-checkout оплаченного t1: %d", len(e.platform.CancellationRequests("t1")))
	if !current.PaymentStarted && len(e.platform.CancellationRequests("t1")) > 0 {
		t.Fatalf("после потерянного ответа оплаченная группа осталась без блокировки и получила отмену")
	}
}

// Черновик с оплаченной группой: повторное подтверждение и оформление не должны создать или оплатить t1 второй раз.
func TestPaidPurchaseCannotCreateOrPayRemovedGroupAgain(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	state, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	e.platform.SetOrderStatus(state.Groups[0].OrderID, checkout.GroupPaid, "")
	if err := announceOrder(ctx, e, state.Groups[0].TenantID, state.ID, state.Groups[0].OrderID); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.RemoveGroup(ctx, buyerID, purchase.ID, "t2"); err != nil {
		t.Fatal(err)
	}
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	confirmed, confirmErr := e.service.Confirm(ctx, buyerID, purchase.ID)
	logPaymentState(t, "Confirm черновика с оплаченным t1", confirmed, confirmErr)
	ordered, orderErr := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	logPaymentState(t, "CreateOrders", ordered, orderErr)
	payErr := e.service.ConfirmPayment(ctx, buyerID, purchase.ID)
	final, _ := e.service.Get(ctx, buyerID, purchase.ID)
	logPaymentState(t, "ConfirmPayment", final, payErr)
	t.Logf(
		"создано заказов t1: %d; оплат: %v",
		len(e.platform.OrderRequests("t1")),
		e.platform.StubPaymentCalls(),
	)
	if len(e.platform.OrderRequests("t1")) > 1 || len(e.platform.StubPaymentCalls()) > 0 {
		t.Fatalf("оплаченная группа создана или оплачена повторно")
	}
}

func logPaymentState(t *testing.T, label string, state checkout.State, err error) {
	t.Helper()
	t.Log(label, state.Status, state.Total, err)
}
