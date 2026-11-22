package checkout_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestSellerDeliveryCostDoesNotChangeBuyerPayment(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	e.platform.SetQuote("t1", delivery.Offered{
		Available: true,
		Cost:      money.FromKopecks(20000),
		MinDays:   2,
		MaxDays:   4,
	})
	state, err := beginAndExecute(context.Background(), e, buyerID, c.ID, orderContact)
	if err != nil || state.Status != checkout.StatusAwaitingPayment ||
		state.DeliveryTotal.Kopecks() != 0 || state.Total != state.GoodsTotal ||
		state.Groups[0].Quote.Cost.Kopecks() != 20000 || e.platform.OrderCount() != 2 {
		t.Fatalf("тариф продавца не меняет сумму покупателя: %+v %v", state, err)
	}
}

func TestDeliveryUnavailableIsTerminalAndReturnsGroup(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	e.platform.FailCreate("t1", &ports.OrderRefusal{Code: "DELIVERY_UNAVAILABLE"})
	ctx := context.Background()
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if state.Status != checkout.StatusDraft || e.platform.OrderCount() != 0 {
		t.Fatal("нужно новое подтверждение", state.Status, err)
	}
	state = reconfirmPurchase(t, e, c.ID)
	if err != nil || state.Groups[0].Status != checkout.GroupDeliveryUnavailable || !state.Groups[0].CartReturned ||
		state.Groups[1].Status != checkout.GroupAwaitingPayment {
		t.Fatalf("окончательный отказ: %+v %v", state, err)
	}
	e.clock.Advance(2 * time.Minute)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	if len(e.platform.OrderRequests("t1")) != 1 {
		t.Fatal("недоступная доставка не повторяется")
	}
	basket, err := e.carts.Get(ctx, buyerID)
	if err != nil || len(basket.Lines) != 1 || basket.Lines[0].Offer != p1 {
		t.Fatalf("товары возвращены: %+v %v", basket, err)
	}
}

func TestUnknownCreationErrorRetriesOnlyBeforeDeadline(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	e.platform.FailCreate("t1", &ports.OrderRefusal{Code: "UNKNOWN_FAILURE"})
	ctx := context.Background()
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	for {
		state, err := e.service.Get(ctx, buyerID, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !state.Groups[0].NextCreateAt.Before(state.Groups[0].CreationDeadline) {
			break
		}
		e.clock.Advance(state.Groups[0].NextCreateAt.Sub(e.clock.Now()))
		if err := resumeAndExecute(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	beforeDeadline := len(e.platform.OrderRequests("t1"))
	if beforeDeadline < 2 {
		t.Fatal("временный отказ должен повторяться")
	}
	for range 3 {
		e.clock.Advance(time.Hour)
		if err := resumeAndExecute(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusDraft || state.Groups[0].Excluded() ||
		state.Groups[0].Reason != "creation_timeout" ||
		len(e.platform.OrderRequests("t1")) != beforeDeadline {
		t.Fatalf("ограничение повторов: %+v %v", state, err)
	}
}

func TestPartialCartReturnDoesNotBlockShippedOrPayment(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	// Покупатель успел добавить 998 из 999 допустимых единиц. Вернётся одна из двух.
	e.store.SeedCart(
		cart.State{
			ID:        buyerID,
			BuyerID:   buyerID,
			Version:   1,
			ExpiresAt: t0.Add(24 * time.Hour),
			Lines:     []cart.Line{{Offer: p1, Name: "Товар", Price: money.FromKopecks(199000), Quantity: 998}},
		},
	)
	e.platform.SetOrderStatus(state.Groups[0].OrderID, checkout.GroupCancelled, "payment_expired")
	if err := announceOrder(ctx, e, "t1", c.ID, state.Groups[0].OrderID); err != nil {
		t.Fatal(err)
	}
	if _, err := refreshOrders(ctx, e, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Pay(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	e.platform.SetOrderStatus(state.Groups[1].OrderID, checkout.GroupPaid, "")
	if err := announceOrder(ctx, e, "t2", c.ID, state.Groups[1].OrderID); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(30 * time.Second)
	if _, err := refreshOrders(ctx, e, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	e.platform.SetOrderStatus(state.Groups[1].OrderID, checkout.GroupShipped, "")
	e.clock.Advance(10 * time.Minute)
	if err := announceOrder(ctx, e, state.Groups[1].TenantID, c.ID, state.Groups[1].OrderID); err != nil {
		t.Fatal(err)
	}
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	state, err = e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[1].Status != checkout.GroupShipped || !state.Groups[0].ReturnFailed ||
		state.Groups[0].ReturnReason != "cart.invalid_quantity" || state.Groups[0].ReturnRemaining[0].Quantity != 1 {
		t.Fatalf("сверка не блокируется остатком: %+v %v", state, err)
	}
	basket, err := e.carts.Get(ctx, buyerID)
	if err != nil || basket.Lines[0].Quantity != 999 {
		t.Fatalf("вернулось сколько поместилось: %+v %v", basket, err)
	}
	if err := e.service.ReturnCancelled(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	basket, _ = e.carts.Get(ctx, buyerID)
	if basket.Lines[0].Quantity != 999 {
		t.Fatal("повтор не дублирует уже возвращённое")
	}
	// После освобождения места возвращается только сохранённый остаток.
	basket.Lines[0].Quantity = 998
	e.store.SeedCart(basket)
	e.clock.Advance(time.Minute)
	if err := e.service.ReturnCancelled(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	state, _ = e.service.Get(ctx, buyerID, c.ID)
	if !state.Groups[0].CartReturned || state.Groups[0].ReturnFailed {
		t.Fatal(state.Groups[0])
	}
}

func TestFailedPollBackoffAndRecovery(t *testing.T) {
	for _, invalidStatus := range []bool{false, true} {
		e := setup(t)
		c := confirmedPurchase(t, e)
		ctx := context.Background()
		state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
		if err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(state.Groups[0].ExpiresAt.Add(checkout.PaymentExpiryGrace).Sub(e.clock.Now()))
		firstID := state.Groups[0].OrderID
		if invalidStatus {
			e.platform.SetOrderStatus(firstID, "unknown_status", "")
		} else {
			e.platform.FailPoll("t1", errors.New("сервер не отвечает"))
		}
		previousDelay := time.Duration(0)
		for i := 1; i <= checkout.MaxOrderFailures; i++ {
			state, err = refreshOrders(ctx, e, buyerID, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			delay := state.Groups[0].NextPollAt.Sub(e.clock.Now())
			if state.Groups[0].PollFailures != i || delay <= previousDelay {
				t.Fatalf("задержка: %+v", state.Groups[0])
			}
			calls := e.platform.PollCalls("t1")
			for range 5 {
				if _, err := refreshOrders(ctx, e, buyerID, c.ID); err != nil {
					t.Fatal(err)
				}
			}
			if e.platform.PollCalls("t1") != calls {
				t.Fatal("GET обошёл NextPollAt")
			}
			previousDelay = delay
			if i < checkout.MaxOrderFailures {
				e.clock.Advance(delay)
			}
		}
		if state.Groups[0].Status != checkout.GroupSellerUnreachable || previousDelay != checkout.OrderSlowPoll {
			t.Fatal(state.Groups[0])
		}
		e.platform.FailPoll("t1", nil)
		e.platform.SetOrderStatus(firstID, checkout.GroupPaid, "")
		e.clock.Advance(checkout.OrderSlowPoll)
		state, err = refreshOrders(ctx, e, buyerID, c.ID)
		if err != nil || state.Groups[0].Status != checkout.GroupPaid || state.Groups[0].PollFailures != 0 {
			t.Fatalf("восстановление сверки: %+v %v", state, err)
		}
	}
}

func TestConcurrentBeginOrdersUsesCASAndSingleSetOfRequests(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	arrived := make(chan struct{}, 4)
	release := make(chan struct{})
	e.platform.CallHook = func(_ context.Context, operation string) error {
		if operation == "quote" {
			arrived <- struct{}{}
			<-release
		}
		return nil
	}
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := beginAndExecute(context.Background(), e, buyerID, c.ID, orderContact)
			failures <- err
		}()
	}
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(3 * time.Second):
			close(release)
			wg.Wait()
			t.Fatal("оба обработчика должны дойти до расчёта до CAS")
		}
	}
	close(release)
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil && !errors.Is(err, checkout.ErrConflict) {
			t.Fatal(err)
		}
	}
	if err := resumeAndExecute(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []ids.TenantID{"t1", "t2"} {
		if len(e.platform.OrderRequests(tenant)) != 1 {
			t.Fatalf("магазин %s получил повторные команды", tenant)
		}
	}
	if e.platform.OrderCount() != 2 {
		t.Fatal("нужен один набор заказов")
	}
}

func TestCreationRetryKeepsIdenticalBodyAfterSettingsChange(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	e.platform.FailCreate("t1", errors.New("таймаут"))
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	original := e.platform.OrderRequests("t1")[0]
	settings, err := e.platform.MarketplaceSettings(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	e.platform.SetSettings("t1", settings)
	e.platform.FailCreate("t1", nil)
	e.clock.Advance(time.Second)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	requests := e.platform.OrderRequests("t1")
	if len(requests) != 2 || !reflect.DeepEqual(original, requests[1]) {
		t.Fatalf("тело изменилось: %+v", requests)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].Status != checkout.GroupAwaitingPayment {
		t.Fatalf("повтор сохраняет бесплатную доставку и прежний ПВЗ: %+v %v", state, err)
	}
}
