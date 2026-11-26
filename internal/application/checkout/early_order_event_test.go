package checkout_test

import (
	"context"
	"testing"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// earlyRejections — события оформления «раннее состояние отвергнуто» по магазину.
type earlyRejections struct {
	tenants []ids.TenantID
}

// earlyOrderEvent — событие отмены заказа магазином, о котором площадка ещё не знает номера.
func earlyOrderEvent(
	id ids.UUID,
	tenant ids.TenantID,
	status checkout.GroupStatus,
	revision int64,
) appcheckout.OrderEvent {
	return appcheckout.OrderEvent{
		EventID:    "orders:early:" + string(status),
		TenantID:   tenant,
		CheckoutID: id,
		Order: checkout.SellerOrder{
			OrderID:  "order-1",
			Status:   status,
			Reason:   "Заказ отменён магазином",
			Revision: revision,
		},
	}
}

// lostCreation — покупка, чей первый ответ создания потерян: у магазина заказ есть, у площадки нет.
func lostCreation(t *testing.T, e *env) (checkout.State, func()) {
	t.Helper()
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	task := firstCreationTask(t, e)
	service := e.service
	e.service = checkoutWithLostReply(e)
	if err := e.service.HandleOrderTask(ctx, task); !appcheckout.CreationPending(err) {
		t.Fatal("потерянный ответ должен оставить фоновый повтор", err)
	}
	e.service = service
	retry := func() {
		t.Helper()
		e.clock.Advance(checkout.CreationRetryDelay)
		if err := e.service.HandleOrderTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	return c, retry
}

// Ответ создания потерян, магазин успел отменить заказ: событие подтверждается сразу (приём не ждёт),
// а отмена применяется той же транзакцией, что сохраняет ответ повторного создания.
func TestOrderEventBeforeCreationReplyAppliesOnAttach(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c, retry := lostCreation(t, e)
	tenant := c.Groups[0].TenantID
	if err := e.service.ApplyOrderEvent(ctx, earlyOrderEvent(c.ID, tenant, checkout.GroupCancelled, 2)); err != nil {
		t.Fatal("раннее событие подтверждается сразу, без повтора приёма", err)
	}
	held, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || held.Groups[0].EarlyOrders == nil || held.Groups[0].OrderID != "" {
		t.Fatalf("раннее состояние хранится при группе до привязки: %v %+v", err, held.Groups[0])
	}
	retry()
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].Status != checkout.GroupCancelled || state.Groups[0].EarlyOrders != nil {
		t.Fatalf("привязка заказа применяет раннюю отмену без нового события: %v %+v", err, state.Groups[0])
	}
}

// Старшая ревизия перекрывает младшую и до привязки, и после неё.
func TestEarlyOrderEventKeepsNewestRevision(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c, retry := lostCreation(t, e)
	tenant := c.Groups[0].TenantID
	for _, event := range []appcheckout.OrderEvent{
		earlyOrderEvent(c.ID, tenant, checkout.GroupCancelled, 3),
		earlyOrderEvent(c.ID, tenant, checkout.GroupAwaitingPayment, 2),
	} {
		if err := e.service.ApplyOrderEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	held, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || held.Groups[0].EarlyOrders == nil || held.Groups[0].EarlyOrders[0].Revision != 3 {
		t.Fatalf("младшая ревизия не перекрывает старшую до привязки: %v %+v", err, held.Groups[0].EarlyOrders)
	}
	retry()
	late := earlyOrderEvent(c.ID, tenant, checkout.GroupAwaitingPayment, 2)
	late.EventID = "orders:late"
	if err := e.service.ApplyOrderEvent(ctx, late); err != nil {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].Status != checkout.GroupCancelled {
		t.Fatalf("младшая ревизия не перекрывает старшую после привязки: %v %+v", err, state.Groups[0])
	}
}

// Группа, оставленная без заказа после срока создания, события не ждёт.
func TestOrderEventForAbandonedGroupWithoutOrderIsAcknowledged(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(state.Groups[0].CreationDeadline.Sub(e.clock.Now()))
	err = e.service.ApplyOrderEvent(ctx, earlyOrderEvent(c.ID, state.Groups[0].TenantID, checkout.GroupCancelled, 2))
	if err != nil {
		t.Fatal("после срока создания событие не держит приём", err)
	}
}

func (r *earlyRejections) RecordCheckoutEvent(tenant ids.TenantID, _ ids.UUID, event string) {
	if event == "early_order_rejected" {
		r.tenants = append(r.tenants, tenant)
	}
}

func (*earlyRejections) RecordCheckoutReadFailure(context.Context, ids.TenantID, ids.UUID, error, bool) {
}

// Отмена без причины противоречит договору и после привязки: отказ раннего состояния доходит до
// счётчика событий оформления, а не исчезает.
func TestEarlyOrderRejectedOnAttachIsCounted(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	recorder := &earlyRejections{}
	e.service.SetObserver(recorder)
	c, retry := lostCreation(t, e)
	tenant := c.Groups[0].TenantID
	event := earlyOrderEvent(c.ID, tenant, checkout.GroupCancelled, 2)
	event.Order.Reason = ""
	if err := e.service.ApplyOrderEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	retry()
	if len(recorder.tenants) != 1 || recorder.tenants[0] != tenant {
		t.Fatalf("отказ раннего состояния учтён: %v", recorder.tenants)
	}
}
