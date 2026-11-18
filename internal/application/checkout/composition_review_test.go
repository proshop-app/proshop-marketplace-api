package checkout_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func logGroups(t *testing.T, state checkout.State) {
	t.Helper()
	for _, g := range state.Groups {
		quote := "<nil>"
		if g.Quote != nil {
			quote = string(g.Quote.Status) + "/" + g.Quote.Reason
		}
		t.Logf(
			"  group %s status=%s effective=%s quote=%s exhausted=%v reconcile=%v order=%q goods=%d",
			g.TenantID,
			g.Status,
			g.EffectiveStatus(),
			quote,
			g.CreateExhausted,
			g.ReconciliationRequired,
			g.OrderID,
			g.Goods.Kopecks(),
		)
	}
}

// H1: покупатель подтвердил обе группы доступными; на повторном расчёте в CreateOrders
// магазин t2 временно не ответил (или вернул no_rate). Контракт: «изменившийся расчёт
// возвращает draft и DELIVERY_CHANGED», «покупатель подтверждает частичное оформление».
func TestAvailabilityLossRequiresNewConfirmation(t *testing.T) {
	for _, mode := range []string{"seller_unreachable", "no_rate"} {
		t.Run(
			mode,
			func(t *testing.T) {
				e := setup(t)
				ctx := context.Background()
				state := confirmedPurchase(t, e)
				t.Logf("после Confirm: status=%s", state.Status)
				logGroups(t, state)
				if mode == "seller_unreachable" {
					e.platform.Fail("t2", errors.New("timeout"))
				} else {
					e.platform.SetQuote("t2", delivery.Offered{Reason: delivery.ReasonNoRate})
				}
				got, err := e.service.CreateOrders(ctx, buyerID, state.ID, orderContact)
				t.Logf("CreateOrders: err=%v status=%s", err, got.Status)
				logGroups(t, got)
				if mode == "seller_unreachable" {
					if !errors.Is(err, checkout.ErrSellerUnavailable) || got.Status != checkout.StatusConfirmed ||
						got.Groups[1].Quote.Status != delivery.QuoteAvailable {
						t.Fatal("временный сбой изменил состав", got.Status, err)
					}
					return
				}
				if !errors.Is(err, checkout.ErrDeliveryChanged) || got.Status != checkout.StatusDraft {
					t.Fatalf("группа, подтверждённая доступной, молча исключена без DELIVERY_CHANGED")
				}
			},
		)
	}
}

// H2: истёкший расчёт пересчитывается внутри Confirm; группа стала недоступной,
// Confirm всё равно успешен, а витрина сразу вызывает CreateOrders (useCheckoutPayment.createOrders).
func TestRequoteAvailabilityLossReturnsDraft(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state, err := e.service.Create(ctx, buyerID, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Quote(ctx, buyerID, state.ID, "cdek", "MSK1", "Город"); err != nil {
		t.Fatal(err)
	}
	// Расчёт t2 живёт 10 минут (ExpiresAt магазина), покупатель задержался на странице.
	e.clock.Advance(11 * time.Minute)
	e.platform.Fail("t2", nil)
	e.platform.SetQuote("t2", delivery.Offered{Reason: delivery.ReasonNoRate})
	confirmed, err := e.service.Confirm(ctx, buyerID, state.ID)
	t.Logf("Confirm: err=%v status=%s", err, confirmed.Status)
	logGroups(t, confirmed)
	orders, err := e.service.CreateOrders(ctx, buyerID, state.ID, orderContact)
	t.Logf("CreateOrders сразу после Confirm: err=%v status=%s", err, orders.Status)
	logGroups(t, orders)
	if err == nil && orders.Status == checkout.StatusCreatingOrders {
		for _, g := range orders.Groups {
			if g.TenantID == "t2" && g.CreateExhausted {
				t.Fatalf("t2 исключена без отдельного подтверждения покупателем частичного состава")
			}
		}
	}
}

// H3: отзыв готовности одного продавца после создания заказов блокирует оплату всех групп;
// исключения группы или отмены по cancel-by-checkout не происходит.
func TestSellerWithdrawalRequiresConsentBeforePayment(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := confirmedPurchase(t, e)
	state, err := beginAndExecute(ctx, e, buyerID, state.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("после создания: status=%s", state.Status)
	logGroups(t, state)
	p, _ := e.store.Participant("t1")
	p.Stage = participant.StageDisconnected
	e.store.SeedParticipant(p)

	_, payErr := e.service.Pay(ctx, buyerID, state.ID)
	confirmErr := e.service.ConfirmPayment(ctx, buyerID, state.ID)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	after, err := e.service.Get(ctx, buyerID, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	amount, amountErr := appcheckout.PaymentAmount(after)
	t.Logf(
		"Pay: %v; ConfirmPayment: %v; после Resume status=%s остаток=%d (%v); оплат=%d; отмен t1=%d",
		payErr,
		confirmErr,
		after.Status,
		amount.Kopecks(),
		amountErr,
		len(e.platform.StubPaymentCalls()),
		len(e.platform.CancellationRequests("t1")),
	)
	logGroups(t, after)
	if errors.Is(payErr, checkout.ErrOfferUnavailable) && len(e.platform.StubPaymentCalls()) == 0 {
		t.Fatalf("здоровую группу t2 нельзя оплатить из-за отзыва готовности t1")
	}
}

// H5: State.Total (поле total, его показывает MockCashier) включает товары исключённой группы,
// а реальная сумма оплаты (payableTotal/PaymentAmount) — нет.
func TestTotalExcludesUnavailableGroups(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	p, _ := e.store.Participant("t1")
	p.Delivery.Carriers = nil
	e.store.SeedParticipant(p)
	state, err := e.service.Create(ctx, buyerID, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.QuoteGroup(ctx, buyerID, state.ID, "t2", "cdek", "MSK1", "Город"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Confirm(ctx, buyerID, state.ID); err != nil {
		t.Fatal(err)
	}
	state, err = beginAndExecute(ctx, e, buyerID, state.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	amount, err := appcheckout.PaymentAmount(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf(
		"status=%s total=%d payable=%d goodsTotal=%d",
		state.Status,
		state.Total.Kopecks(),
		amount.Kopecks(),
		state.GoodsTotal.Kopecks(),
	)
	logGroups(t, state)
	if state.Total.Cmp(amount) != 0 {
		t.Fatalf("total=%d (показывает касса витрины) != к оплате %d", state.Total.Kopecks(), amount.Kopecks())
	}
}

// Неоформляемая группа остаётся в корзине сразу после создания остальных заказов.
func TestExcludedGroupRemainsInCart(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	p, _ := e.store.Participant("t1")
	p.Delivery.Carriers = nil
	e.store.SeedParticipant(p)
	state, err := e.service.Create(ctx, buyerID, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.QuoteGroup(ctx, buyerID, state.ID, "t2", "cdek", "MSK1", "Город"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Confirm(ctx, buyerID, state.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.CreateOrders(ctx, buyerID, state.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	basket, err := e.carts.Get(ctx, buyerID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("корзина сразу после ответа CreateOrders: %+v", basket.Lines)
	found := false
	for _, line := range basket.Lines {
		found = found || line.Offer == p1
	}
	if !found {
		t.Fatalf("товары заведомо исключённой группы t1 изъяты из корзины до асинхронного возврата")
	}
}
