package checkout_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestChangedDispatchOriginRequiresNewConsentBeforeOrderRetry(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.carts.Remove(ctx, buyerID, p2); err != nil {
		t.Fatal(err)
	}
	purchase := confirmedPurchase(t, e)
	started, err := e.service.CreateOrders(ctx, buyerID, purchase.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	beforeQuotes := len(e.platform.QuoteRequests())
	e.platform.FailCreate("t1", &ports.OrderRefusal{Code: checkout.DispatchOriginChanged, DispatchReviewRevision: 2})
	e.platform.SetQuote("t1", delivery.Offered{Available: true, Cost: money.FromKopecks(90000), MinDays: 7, MaxDays: 9})
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	review, err := e.service.Get(ctx, buyerID, purchase.ID)
	if err != nil || review.Status != checkout.StatusDraft ||
		review.Groups[0].Status != checkout.GroupDeliveryRequoted ||
		review.Groups[0].Reason != checkout.DispatchOriginChanged || review.Groups[0].Excluded() ||
		review.Groups[0].Quote.MinDays != 7 || review.Groups[0].Quote.MaxDays != 9 ||
		!review.Groups[0].DeliveryPrice.IsZero() || review.Total != purchase.Total {
		t.Fatal(review, err)
	}
	if len(e.platform.QuoteRequests()) != beforeQuotes+1 {
		t.Fatal("нужен один пересчёт")
	}
	recalculation := e.platform.QuoteRequests()[beforeQuotes].DispatchReview
	if recalculation == nil || recalculation.Revision != 2 || recalculation.GroupID != "t1" ||
		recalculation.ExternalCheckoutID != started.Groups[0].ExternalCheckoutID {
		t.Fatal("пересчёт должен подтверждать сохранённый отказ", recalculation)
	}
	if e.platform.OrderCount() != 0 {
		t.Fatal("тихое повторное создание")
	}
	e.clock.Advance(appcheckout.CreationTimeout + time.Second)
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	if e.platform.OrderCount() != 0 {
		t.Fatal("повтор без согласия покупателя")
	}
	e.platform.FailCreate("t1", nil)
	final := reconfirmPurchase(t, e, purchase.ID)
	if final.Groups[0].OrderID == "" || final.Groups[0].ExternalCheckoutID != started.Groups[0].ExternalCheckoutID {
		t.Fatal(final)
	}
	repeated, err := e.service.CreateOrders(ctx, buyerID, purchase.ID, orderContact)
	if err != nil || repeated.Groups[0].OrderID != final.Groups[0].OrderID || e.platform.OrderCount() != 1 {
		t.Fatal(repeated, err)
	}
}

func TestFailedDispatchRecalculationCanBeRetriedOnlyWithSamePickup(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.carts.Remove(ctx, buyerID, p2); err != nil {
		t.Fatal(err)
	}
	purchase := confirmedPurchase(t, e)
	if _, err := e.service.CreateOrders(ctx, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.platform.FailCreate("t1", &ports.OrderRefusal{Code: checkout.DispatchOriginChanged, DispatchReviewRevision: 2})
	e.platform.Fail("t1", ports.ErrTemporaryOrderRefusal)
	// Первая команда должна сообщить смену склада, а расчёт после неё — временный сбой.
	e.platform.CallHook = func(_ context.Context, operation string) error {
		if operation == "create_order" {
			return &ports.OrderRefusal{Code: checkout.DispatchOriginChanged, DispatchReviewRevision: 2}
		}
		return nil
	}
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	e.platform.CallHook = nil
	e.platform.Fail("t1", nil)
	e.platform.FailCreate("t1", nil)
	if _, err := e.service.Quote(ctx, buyerID, purchase.ID, "cdek", "OTHER", "Город"); err == nil {
		t.Fatal("нельзя изменить подтверждённый ПВЗ")
	}
	state, err := e.service.Quote(ctx, buyerID, purchase.ID, "cdek", "MSK1", "Город")
	if err != nil || state.Status != checkout.StatusDraft || state.Groups[0].Quote.Status != delivery.QuoteAvailable {
		t.Fatal(state, err)
	}
}

func TestDispatchReviewPreservesOtherShopOrderAcrossNewConsent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	if _, err := e.service.CreateOrders(ctx, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"BuyerID": buyerID, "CheckoutID": purchase.ID})
	if err := e.service.HandleOrderTask(ctx, ports.Task{
		Kind:     appcheckout.TaskCreateOrder,
		TenantID: "t2",
		Payload:  payload,
	}); err != nil {
		t.Fatal(err)
	}
	e.platform.FailCreate("t1", &ports.OrderRefusal{
		Code:                   checkout.DispatchOriginChanged,
		DispatchReviewRevision: 1,
	})
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	review, err := e.service.Get(ctx, buyerID, purchase.ID)
	if err != nil || review.Status != checkout.StatusDraft || review.Groups[1].OrderID == "" {
		t.Fatal(review, err)
	}
	preserved := review.Groups[1].OrderID
	e.platform.FailCreate("t1", nil)
	final := reconfirmPurchase(t, e, purchase.ID)
	if final.Groups[1].OrderID != preserved || final.Groups[0].OrderID == "" ||
		len(e.platform.OrderRequests("t2")) != 1 || e.platform.OrderCount() != 2 {
		t.Fatal("заказ здорового магазина не должен пересоздаваться", final)
	}
}

func TestRemovingDispatchReviewCancelsShopAttemptByCheckoutKey(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.carts.Remove(ctx, buyerID, p2); err != nil {
		t.Fatal(err)
	}
	purchase := confirmedPurchase(t, e)
	if _, err := e.service.CreateOrders(ctx, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.platform.FailCreate("t1", &ports.OrderRefusal{
		Code:                   checkout.DispatchOriginChanged,
		DispatchReviewRevision: 1,
	})
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.RemoveGroup(ctx, buyerID, purchase.ID, "t1"); err != nil {
		t.Fatal(err)
	}
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	requests := e.platform.CancellationRequests("t1")
	if len(requests) != 1 || requests[0].ExternalCheckoutID != purchase.Groups[0].ExternalCheckoutID {
		t.Fatal("удаление должно навсегда закрыть попытку магазина", requests)
	}
}

func TestUnavailableDispatchRecalculationReturnsGoodsAndClosesShopKey(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.carts.Remove(ctx, buyerID, p2); err != nil {
		t.Fatal(err)
	}
	purchase := confirmedPurchase(t, e)
	if _, err := e.service.CreateOrders(ctx, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.platform.FailCreate("t1", &ports.OrderRefusal{
		Code:                   checkout.DispatchOriginChanged,
		DispatchReviewRevision: 1,
	})
	e.platform.SetQuote("t1", delivery.Offered{Available: false, Reason: delivery.ReasonNoRate})
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, purchase.ID)
	if err != nil || state.Status != checkout.StatusDraft || !state.Groups[0].Excluded() ||
		!state.Groups[0].CartReturned || !state.Total.IsZero() || len(e.platform.CancellationRequests("t1")) != 1 {
		t.Fatal(state, err)
	}
}
