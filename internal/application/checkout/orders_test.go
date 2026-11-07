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
	if _, err := e.service.Quote(ctx, buyerID, state.ID, "cdek", "MSK1"); err != nil {
		t.Fatal(err)
	}
	state, err = e.service.Confirm(ctx, buyerID, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestResumeAfterOrderCreatedBeforeCommit(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	crash := errors.New("падение перед фиксацией")
	e.platform.AfterCreate = func() { e.store.FailCommit = crash }
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); !errors.Is(err, crash) {
		t.Fatal(err)
	}
	if e.platform.OrderCount() != 1 {
		t.Fatal("заказ успел появиться у магазина")
	}
	e.platform.AfterCreate, e.store.FailCommit = nil, nil
	e.clock.Advance(checkout.OrderCallLease)
	if err := e.service.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusAwaitingPayment || e.platform.OrderCount() != 2 {
		t.Fatalf("возобновление без дублей: %+v %v", state, err)
	}
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, checkout.Contact{}); err != nil {
		t.Fatal(err)
	}
	if e.platform.OrderCount() != 2 {
		t.Fatal("повтор не создаёт заказов")
	}
	basket, err := e.carts.Get(ctx, buyerID)
	if err != nil || len(basket.Lines) != 0 {
		t.Fatalf("снимок изъят из корзины один раз: %+v %v", basket, err)
	}
}

func TestSellerFailureDoesNotBlockOtherGroups(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	e.platform.FailCreate("t1", errors.New("магазин не отвечает"))
	state, err := e.service.CreateOrders(context.Background(), buyerID, c.ID, orderContact)
	if err != nil || state.Groups[0].Status != checkout.GroupSellerUnreachable ||
		state.Groups[1].Status != checkout.GroupAwaitingPayment {
		t.Fatalf("независимые группы: %+v %v", state, err)
	}
	e.platform.FailCreate("t1", nil)
	e.clock.Advance(checkout.OrderRetryBase)
	if err := e.service.Resume(context.Background()); err != nil {
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
	state, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact)
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
	state, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	first, second := state.Groups[0], state.Groups[1]
	e.platform.SetOrderStatus(first.OrderID, checkout.GroupPaid, "")
	e.platform.SetOrderStatus(second.OrderID, checkout.GroupCancelled, "payment_expired")
	state, err = e.service.Refresh(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusPartiallyPaid {
		t.Fatalf("сверка: %+v %v", state, err)
	}
	if _, err := e.service.Pay(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal(err)
	}
	if err := e.service.Resume(ctx); err != nil {
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
	if _, err := e.service.CreateOrders(ctx, other, c.ID, orderContact); !errors.Is(err, checkout.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := e.service.Pay(ctx, other, c.ID); !errors.Is(err, checkout.ErrNotFound) {
		t.Fatal(err)
	}
	e.clock.Advance(11 * time.Minute)
	e.platform.SetOrderStatus(first.OrderID, checkout.GroupShipped, "")
	state, err = e.service.Refresh(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].Status != checkout.GroupShipped {
		t.Fatalf("трек: %+v %v", state, err)
	}
}

func TestPaymentExpiryAndStuckFilter(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(16 * time.Minute)
	if _, err := e.service.Refresh(ctx, buyerID, c.ID); err != nil {
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
		p.FreeDeliveryFrom = money.FromKopecks(threshold)
		e.store.SeedParticipant(p)
		c := confirmedPurchase(t, e)
		want := int64(0)
		if threshold > 398000 {
			want = 15000
		}
		if c.Groups[0].DeliveryPrice.Kopecks() != want || c.DeliveryTotal.Kopecks() != want+17500 {
			t.Fatalf("порог %d: %+v", threshold, c)
		}
	}
	e := setup(t)
	p, _ := e.store.Participant("t1")
	p.Delivery.Regions = []string{"RU-SPE"}
	e.store.SeedParticipant(p)
	ctx := context.Background()
	c, _ := e.service.Create(ctx, buyerID, key)
	quoted, err := e.service.Quote(ctx, buyerID, c.ID, "cdek", "MSK1")
	if err != nil || quoted.Groups[0].Quote.Reason != "pickup_point_unavailable" {
		t.Fatalf("регион: %+v %v", quoted, err)
	}
}

func TestInvalidSellerTotalDoesNotBlockOtherSeller(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	crash := errors.New("нет записи ответа")
	e.platform.AfterCreate = func() { e.store.FailCommit = crash }
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); !errors.Is(err, crash) {
		t.Fatal(err)
	}
	e.platform.AfterCreate, e.store.FailCommit = nil, nil
	e.clock.Advance(checkout.OrderCallLease)
	e.platform.SetOrderTotal("order-1", money.FromKopecks(1))
	if err := e.service.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].Status != checkout.GroupSellerUnreachable ||
		state.Groups[1].Status != checkout.GroupAwaitingPayment {
		t.Fatalf("неверная сумма: %+v %v", state, err)
	}
}
