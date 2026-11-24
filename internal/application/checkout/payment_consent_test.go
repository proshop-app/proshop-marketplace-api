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
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

var consentThirdOffer = ids.OfferID{Tenant: "t3", Product: "p1"}

type compositionHook struct {
	once sync.Once
	fn   func()
	ids  fake.IDs
}

// addConsentThirdShop добавляет магазин до создания покупки.
func addConsentThirdShop(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	e.store.SeedBuyerOffer(consentThirdOffer, "Товар", money.FromKopecks(50000))
	point, err := delivery.NewPickupPoint(
		"cdek",
		"MSK1",
		"Пункт",
		"ул. Примерная, 1",
		"Город",
		delivery.Location{Lat: 55.7, Lon: 37.6},
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	point.RegionCode = "RU-MOW"
	e.store.SeedTenant(participant.TenantState{TenantID: "t3", Status: participant.TenantActive})
	e.platform.SetPickupPoints("t3", ports.PickupPoints{Available: true, Items: []delivery.PickupPoint{point}})
	e.store.SeedParticipant(
		participant.State{
			Stage:    participant.StageConnected,
			TenantID: "t3",
			Enabled:  true,
			Delivery: w3ServiceDelivery,
		},
	)
	e.platform.SetSettings("t3", ports.ShopSettings{Delivery: w3ServiceDelivery})
	e.platform.SetItem(
		"t3",
		ports.ItemCheck{
			ProductID: "p1",
			Exists:    true,
			Price: money.FromKopecks(
				50000,
			),
			Available: 5,
		},
	)
	e.platform.SetQuote("t3", delivery.Offered{Available: true, Cost: money.FromKopecks(20000), MinDays: 2, MaxDays: 3})
	if _, err := e.carts.Add(ctx, buyerID, consentThirdOffer, 1); err != nil {
		t.Fatal(err)
	}
}

// partiallyPaidConsentPurchase: три созданных заказа, t1 оплачен (сервер сообщил paid, площадка сверила).
func partiallyPaidConsentPurchase(t *testing.T, e *env) checkout.State {
	t.Helper()
	ctx := context.Background()
	addConsentThirdShop(t, e)
	purchase := confirmedPurchase(t, e)
	state, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Groups) != 3 {
		t.Fatalf("ожидались три группы: %d", len(state.Groups))
	}
	e.platform.SetOrderStatus(state.Groups[0].OrderID, checkout.GroupPaid, "")
	if err := announceOrder(ctx, e, state.Groups[0].TenantID, state.ID, state.Groups[0].OrderID); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	state, _ = e.service.Get(ctx, buyerID, purchase.ID)
	return state
}

// Новое правило: состав изменён после частичной оплаты → новый Confirm, доплата до него запрещена.
// Проверяем, что из этого состояния покупатель выходит: Confirm → оплата t3.
func TestRemoveAfterPartialPaymentRequiresConfirmation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := partiallyPaidConsentPurchase(t, e)
	logPaymentState(t, "t1 оплачен", state, nil)
	removed, err := e.service.RemoveGroup(ctx, buyerID, state.ID, "t2")
	logPaymentState(t, "RemoveGroup t2", removed, err)
	_, payErr := e.service.Pay(ctx, buyerID, state.ID)
	confirmPayErr := e.service.ConfirmPayment(ctx, buyerID, state.ID)
	t.Logf("до Confirm: Pay=%v ConfirmPayment=%v оплат=%v", payErr, confirmPayErr, e.platform.StubPaymentCalls())
	if len(e.platform.StubPaymentCalls()) != 0 {
		t.Fatalf("доплата прошла без нового подтверждения состава")
	}
	confirmed, confirmErr := e.service.Confirm(ctx, buyerID, state.ID)
	logPaymentState(t, "Confirm", confirmed, confirmErr)
	payErr = e.service.ConfirmPayment(ctx, buyerID, state.ID)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	final, _ := e.service.Get(ctx, buyerID, state.ID)
	logPaymentState(t, "после Confirm и оплаты", final, payErr)
	t.Logf(
		"оплаты: %v; отмен t1=%d t2=%d t3=%d; корзина: %s",
		e.platform.StubPaymentCalls(),
		len(e.platform.CancellationRequests("t1")),
		len(e.platform.CancellationRequests("t2")),
		len(e.platform.CancellationRequests("t3")),
		paymentCartContents(t, e),
	)
	if confirmErr != nil || payErr != nil || final.Status != checkout.StatusPaid ||
		len(e.platform.CancellationRequests("t1")) != 0 || len(e.platform.CancellationRequests("t2")) != 1 {
		t.Fatalf("после подтверждения нового состава покупатель не доплатил t3 или оплаченный t1 задет")
	}
}

// Магазин t2 сам отменил свой заказ при оплаченном t1: требуется ли новое согласие перед оплатой t3.
func TestShopCancellationRequiresConfirmationAfterPartialPayment(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := partiallyPaidConsentPurchase(t, e)
	e.platform.SetOrderStatus(state.Groups[1].OrderID, checkout.GroupCancelled, "shop_cancelled")
	if err := announceOrder(ctx, e, state.Groups[1].TenantID, state.ID, state.Groups[1].OrderID); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	after, _ := e.service.Get(ctx, buyerID, state.ID)
	logPaymentState(t, "t2 отменён магазином", after, nil)
	payErr := e.service.ConfirmPayment(ctx, buyerID, state.ID)
	t.Logf(
		"ConfirmPayment без нового Confirm: %v; оплаты: %v; корзина: %s",
		payErr,
		e.platform.StubPaymentCalls(),
		paymentCartContents(
			t,
			e,
		),
	)
	if payErr == nil || len(e.platform.StubPaymentCalls()) > 0 {
		t.Fatalf("магазин выпал после подтверждения, но доплата t3 прошла без повторного подтверждения")
	}
	if _, err := e.service.Confirm(ctx, buyerID, state.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, state.ID); err != nil {
		t.Fatal("после нового согласия доплата должна проходить", err)
	}
	if len(e.platform.StubPaymentCalls()) != 1 || e.platform.StubPaymentCalls()[0] != "t3" {
		t.Fatal(e.platform.StubPaymentCalls())
	}
}

func (h *compositionHook) NewUUID() ids.UUID {
	h.once.Do(h.fn)
	return h.ids.NewUUID()
}

func (h *compositionHook) NewToken() string {
	return h.ids.NewToken()
}

// Окно ConfirmPayment между проверкой суммы и захватом: состав меняется (t2 убран другой вкладкой).
// Захват в CAS обязан проверить новое согласие; оплачиваемый t3 сам не меняется.
func TestCompositionChangePreventsPaymentClaim(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := partiallyPaidConsentPurchase(t, e)
	e.store.SeedVerdict(moderation.VerdictState{TenantID: "t2", ProductID: "p1", Status: moderation.Pending})
	hook := &compositionHook{}
	var removeErr error
	hook.fn = func() {
		_, removeErr = e.service.RemoveGroup(ctx, buyerID, state.ID, "t2")
	}
	svc := appcheckout.NewService(
		e.store,
		e.platform,
		e.pickups,
		e.verifier,
		hook,
		e.clock,
		appcheckout.Config{
			QuoteTTL:      30 * time.Minute,
			QuoteTimeout:  time.Second,
			PaymentStub:   true,
			ReturnBaseURL: "https://market.example",
		},
	)
	payErr := svc.ConfirmPayment(ctx, buyerID, state.ID)
	final, _ := e.service.Get(ctx, buyerID, state.ID)
	logPaymentState(t, "ConfirmPayment при параллельном удалении приостановленного t2", final, payErr)
	t.Logf("RemoveGroup в окне: %v; оплаты: %v", removeErr, e.platform.StubPaymentCalls())
	if removeErr != nil || !errors.Is(payErr, checkout.ErrDeliveryChanged) ||
		len(e.platform.StubPaymentCalls()) != 0 || !checkout.Restore(final).NeedsConfirmation() {
		t.Fatal("захват оплаты принял неподтверждённое изменение состава", removeErr, payErr)
	}
}

// Отмена в ходе другой оплаты сохраняет незавершённый захват и новое требование согласия.
func TestShopCancellationDuringPaymentKeepsConsentRequired(t *testing.T) {
	for _, paidNeighbour := range []bool{false, true} {
		e := setup(t)
		state := partiallyPaidConsentPurchase(t, e)
		if !paidNeighbour {
			state.Groups[0].Status = checkout.GroupAwaitingPayment
			state.Groups[0].LastOrderStatus = checkout.GroupAwaitingPayment
			state.Status = checkout.StatusAwaitingPayment
		}
		purchase := checkout.Restore(state)
		now := e.clock.Now()
		orders := map[ids.TenantID]string{"t3": state.Groups[2].OrderID}
		if err := purchase.ClaimPayment("in-flight", orders, now); err != nil {
			t.Fatal(err)
		}
		cancelled := checkout.SellerOrder{
			OrderID: state.Groups[1].OrderID,
			Status:  checkout.GroupCancelled,
			Reason:  "shop_cancelled",
		}
		if _, err := purchase.Observe("t2", cancelled, now); err != nil {
			t.Fatal(err)
		}
		if !purchase.NeedsConfirmation() || !purchase.State().PaymentStarted ||
			purchase.Status() == checkout.StatusDraft {
			t.Fatal("изменение состава потеряло согласие или незавершённую оплату", purchase.State())
		}
		result := checkout.PaymentResult{Unpaid: map[ids.TenantID]bool{"t3": true}}
		if err := purchase.FinishPayment("in-flight", result, now); err != nil {
			t.Fatal(err)
		}
		if !purchase.NeedsConfirmation() || purchase.State().PaymentStarted {
			t.Fatal("подтверждение отсутствия платежа не заменяет согласие на состав", purchase.State())
		}
	}
}
