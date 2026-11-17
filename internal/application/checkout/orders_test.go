package checkout_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

var orderContact = checkout.Contact{Phone: "79251234567", Name: "Покупатель"}

func confirmedPurchase(t *testing.T, e *env) checkout.State {
	t.Helper()
	ctx := context.Background()
	state, err := e.service.Create(ctx, buyerID, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Quote(ctx, buyerID, state.ID, "cdek", "MSK1", "Город"); err != nil {
		t.Fatal(err)
	}
	state, err = e.service.Confirm(ctx, buyerID, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestCreationTaskRecoversAfterExecutorStopsBeforeCommit(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	crash := errors.New("падение перед фиксацией")
	attemptCtx, stop := context.WithCancel(ctx)
	e.platform.AfterCreate = func() { e.store.FailCommit = crash; stop() }
	if _, err := beginAndExecute(attemptCtx, e, buyerID, c.ID, orderContact); !errors.Is(err, crash) {
		t.Fatal(err)
	}
	if e.platform.OrderCount() != 1 {
		t.Fatal("заказ успел появиться у магазина")
	}
	e.platform.AfterCreate, e.store.FailCommit = nil, nil
	e.clock.Advance(5*time.Second + 100*time.Millisecond)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusAwaitingPayment || e.platform.OrderCount() != 2 {
		t.Fatalf("повтор до срока восстанавливает оба заказа: %+v %v", state, err)
	}
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, checkout.Contact{}); err != nil {
		t.Fatal(err)
	}
	if e.platform.OrderCount() != 2 {
		t.Fatal("повтор не создаёт заказов")
	}

}

func TestSellerFailureDoesNotBlockOtherGroups(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	e.platform.FailCreate("t1", errors.New("магазин не отвечает"))
	state, err := beginAndExecute(context.Background(), e, buyerID, c.ID, orderContact)
	if err != nil || state.Groups[0].Status != checkout.GroupSellerUnreachable ||
		state.Groups[1].Status != checkout.GroupAwaitingPayment {
		t.Fatalf("независимые группы: %+v %v", state, err)
	}
	e.platform.FailCreate("t1", nil)
	e.clock.Advance(time.Second)
	if err := resumeAndExecute(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if e.platform.OrderCount() != 2 {
		t.Fatal("создан только пропущенный заказ")
	}
}

func TestPriceChangedKeepsSnapshotAndReturnsGroup(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	e.platform.SetItem("t1", ports.ItemCheck{
		ProductID: "p1",
		Exists:    true,
		Available: 3,
		Price:     money.FromKopecks(209000),
	})
	ctx := context.Background()
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if state.Status != checkout.StatusDraft || e.platform.OrderCount() != 0 {
		t.Fatal("окончательный отказ должен остановить оформление до нового согласия", state.Status, err)
	}
	state = reconfirmPurchase(t, e, c.ID)
	if err != nil || state.Groups[0].Status != checkout.GroupPriceChanged ||
		state.Groups[0].Lines[0].Price.Kopecks() != 199000 ||
		state.Groups[0].CurrentPrices[0].Price.Kopecks() != 209000 || e.platform.OrderCount() != 1 {
		t.Fatalf("PRICE_CHANGED: %+v %v", state, err)
	}
	if err := e.service.ReturnCancelled(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	basket, err := e.carts.Get(ctx, buyerID)
	if err != nil || len(basket.Lines) != 1 || basket.Lines[0].Quantity != 2 {
		t.Fatalf("возврат: %+v %v", basket, err)
	}
}

func TestPollingAndCancelledCartReturnAreIdempotent(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	first, second := state.Groups[0], state.Groups[1]
	e.platform.SetOrderStatus(first.OrderID, checkout.GroupPaid, "")
	e.platform.SetOrderStatus(second.OrderID, checkout.GroupCancelled, "payment_expired")
	state, err = refreshOrders(ctx, e, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusPartiallyPaid {
		t.Fatalf("сверка: %+v %v", state, err)
	}
	if _, err := e.service.Pay(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal(err)
	}
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	basket, err := e.carts.Get(ctx, buyerID)
	if err != nil || len(basket.Lines) != 1 || basket.Lines[0].Offer != p2 || basket.Lines[0].Quantity != 1 {
		t.Fatalf("отменённая группа возвращена один раз: %+v %v", basket, err)
	}
	purchases, err := e.service.Purchases(ctx, buyerID, "")
	if err != nil || len(purchases) != 1 || purchases[0].State().Groups[0].Status != checkout.GroupPaid {
		t.Fatalf("кабинет: %+v %v", purchases, err)
	}
	other := ids.UUID("11111111-1111-4111-8111-111111111111")
	if _, err := beginAndExecute(ctx, e, other, c.ID, orderContact); !errors.Is(err, checkout.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := e.service.Pay(ctx, other, c.ID); !errors.Is(err, checkout.ErrNotFound) {
		t.Fatal(err)
	}
	e.clock.Advance(11 * time.Minute)
	e.platform.SetOrderStatus(first.OrderID, checkout.GroupShipped, "")
	state, err = refreshOrders(ctx, e, buyerID, c.ID)
	if err != nil || state.Groups[0].Status != checkout.GroupShipped {
		t.Fatalf("трек: %+v %v", state, err)
	}
}

func TestPaymentExpiryAndStuckFilter(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(16 * time.Minute)
	if _, err := refreshOrders(ctx, e, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	stuck, err := e.service.OperatorList(ctx, "", true)
	if err != nil || len(stuck) != 1 {
		t.Fatalf("опрос не сбрасывает время ожидания: %+v %v", stuck, err)
	}
	e.clock.Advance(44 * time.Minute)
	if _, err := e.service.Pay(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrPaymentExpired) {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusAwaitingPayment {
		t.Fatalf("истечение не равно отмене: %+v %v", state, err)
	}
}

func TestDeliveryThresholdsAndRegions(t *testing.T) {
	for _, threshold := range []int64{0, 398000, 398001} {
		e := setup(t)
		p, _ := e.store.Participant("t1")
		e.store.SeedParticipant(p)
		c := confirmedPurchase(t, e)
		want := int64(0)
		if c.Groups[0].DeliveryPrice.Kopecks() != want || c.DeliveryTotal.Kopecks() != 0 {
			t.Fatalf("порог %d: %+v", threshold, c)
		}
	}
	e := setup(t)
	p, _ := e.store.Participant("t1")
	p.Delivery.Regions = []string{"RU-SPE"}
	e.store.SeedParticipant(p)
	ctx := context.Background()
	c, _ := e.service.Create(ctx, buyerID, key)
	quoted, err := e.service.Quote(ctx, buyerID, c.ID, "cdek", "MSK1", "Город")
	if err != nil || quoted.Groups[0].Quote.Reason != "pickup_point_unavailable" {
		t.Fatalf("регион: %+v %v", quoted, err)
	}
}

func TestInvalidSellerTotalDoesNotBlockOtherSeller(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	crash := errors.New("нет записи ответа")
	attemptCtx, stop := context.WithCancel(ctx)
	e.platform.AfterCreate = func() { e.store.FailCommit = crash; stop() }
	if _, err := beginAndExecute(attemptCtx, e, buyerID, c.ID, orderContact); !errors.Is(err, crash) {
		t.Fatal(err)
	}
	e.platform.AfterCreate, e.store.FailCommit = nil, nil
	e.clock.Advance(5*time.Second + 100*time.Millisecond)
	e.platform.SetOrderTotal("order-1", money.FromKopecks(1))
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].Status != checkout.GroupSellerUnreachable ||
		state.Groups[1].Status != checkout.GroupAwaitingPayment {
		t.Fatalf("неверная сумма: %+v %v", state, err)
	}
}
