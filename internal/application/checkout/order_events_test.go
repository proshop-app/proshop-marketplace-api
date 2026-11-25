package checkout_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// errBreaker — отказ ограждения без вызова магазина.
var errBreaker = errors.New("размыкатель открыт")

// paidPurchase — покупка двух магазинов с оплаченными заказами.
func paidPurchase(t *testing.T, e *env) checkout.State {
	t.Helper()
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].Status != checkout.GroupPaid || state.Groups[1].Status != checkout.GroupPaid {
		t.Fatal(state, err)
	}
	return state
}

// groupStatuses — статусы обеих групп покупки в порядке магазинов.
func groupStatuses(t *testing.T, e *env, id ids.UUID) [2]checkout.GroupStatus {
	t.Helper()
	state, err := e.service.Get(context.Background(), buyerID, id)
	if err != nil {
		t.Fatal(err)
	}
	return [2]checkout.GroupStatus{state.Groups[0].Status, state.Groups[1].Status}
}

func TestOrderEventIsAppliedOnce(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := paidPurchase(t, e)
	first := state.Groups[0]
	event := appcheckout.OrderEvent{
		EventID:    "orders:0:1",
		TenantID:   first.TenantID,
		CheckoutID: state.ID,
		Order:      checkout.SellerOrder{OrderID: first.OrderID, Status: checkout.GroupAssembled, Revision: 5},
	}
	if err := e.service.ApplyOrderEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if groupStatuses(t, e, state.ID) != [2]checkout.GroupStatus{checkout.GroupAssembled, checkout.GroupPaid} {
		t.Fatal("событие меняет только свой заказ")
	}
	event.Order.Status, event.Order.Revision = checkout.GroupShipped, 6
	if err := e.service.ApplyOrderEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if groupStatuses(t, e, state.ID)[0] != checkout.GroupAssembled {
		t.Fatal("повтор доставки того же сообщения не применяется дважды")
	}
	polls := e.platform.PollCalls(first.TenantID)
	if err := resumeAndExecute(ctx, e); err != nil || e.platform.PollCalls(first.TenantID) != polls {
		t.Fatal("после события магазин не опрашивается", err)
	}
}

func TestOrderEventForUnknownPurchaseIsNotFailure(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := paidPurchase(t, e)
	first := state.Groups[0]
	foreign := appcheckout.OrderEvent{
		EventID:    "orders:0:1",
		TenantID:   first.TenantID,
		CheckoutID: ids.UUID("11111111-1111-4111-8111-111111111111"),
		Order:      checkout.SellerOrder{OrderID: first.OrderID, Status: checkout.GroupShipped, Revision: 5},
	}
	if err := e.service.ApplyOrderEvent(ctx, foreign); err != nil {
		t.Fatal("заказ другой установки не должен останавливать приём", err)
	}
	other := foreign
	other.EventID, other.CheckoutID, other.Order.OrderID = "orders:0:2", state.ID, "другой-заказ"
	err := e.service.ApplyOrderEvent(ctx, other)
	if err != nil || groupStatuses(t, e, state.ID)[0] != checkout.GroupPaid {
		t.Fatal("чужой заказ не меняет группу", err)
	}
}

func TestShopCancellationEventDoesNotReplaceBuyerCancellation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Cancel(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	before, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || before.Groups[0].OrderID == "" {
		t.Fatal(before, err)
	}
	first := before.Groups[0]
	err = e.service.ApplyOrderEvent(ctx, appcheckout.OrderEvent{
		EventID:    "orders:0:1",
		TenantID:   first.TenantID,
		CheckoutID: c.ID,
		Order: checkout.SellerOrder{
			OrderID:  first.OrderID,
			Status:   checkout.GroupCancelled,
			Reason:   "Заказ отменён магазином",
			Revision: 2,
		},
	})
	after, getErr := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || getErr != nil || after.Version != before.Version || after.Groups[0].Reason != first.Reason {
		t.Fatalf("отмену покупателя ведёт сценарий площадки, событие её не меняет: %v %+v", err, after.Groups[0])
	}
}

func TestOrderEventAgainstOrderFlowIsPoison(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := paidPurchase(t, e)
	first := state.Groups[0]
	event := func(id string, status checkout.GroupStatus, revision int64) appcheckout.OrderEvent {
		return appcheckout.OrderEvent{
			EventID:    id,
			TenantID:   first.TenantID,
			CheckoutID: state.ID,
			Order:      checkout.SellerOrder{OrderID: first.OrderID, Status: status, Revision: revision},
		}
	}
	// Оплату площадка поставила сама, без номера перехода: отставшее событие — не противоречие.
	if err := e.service.ApplyOrderEvent(ctx, event("orders:0:1", checkout.GroupAwaitingPayment, 1)); err != nil {
		t.Fatal("опоздавшее событие создания не должно уходить в отравленные", err)
	}
	if err := e.service.ApplyOrderEvent(ctx, event("orders:0:2", checkout.GroupAwaitingPickup, 6)); err != nil {
		t.Fatal(err)
	}
	err := e.service.ApplyOrderEvent(ctx, event("orders:0:3", checkout.GroupShipped, 7))
	if !errors.Is(err, appcheckout.ErrOrderEventRejected) || errs.RetryKindOf(err) != errs.RetryPermanent {
		t.Fatal("регресс с новым номером перехода — отравленное сообщение, а не бесконечный повтор", err)
	}
	if groupStatuses(t, e, state.ID)[0] != checkout.GroupAwaitingPickup {
		t.Fatal("отклонённое событие не меняет заказ")
	}
}

func TestReconcileDoesNotReportUnappliedStateAsFixed(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := paidPurchase(t, e)
	first := state.Groups[0]
	// Площадка считает заказ оплаченным, магазин — отменённым неоплаченным: такое не применяется.
	e.platform.SetOrderStatus(first.OrderID, checkout.GroupCancelled, "Заказ отменён магазином")

	result, err := e.service.ReconcileOrders(ctx)
	if err != nil || result.Mismatched != 0 || result.Unresolved != 1 {
		t.Fatalf("неприменённое расхождение — сигнал оператору, а не исправление: %+v %v", result, err)
	}
	if groupStatuses(t, e, state.ID)[0] != checkout.GroupPaid {
		t.Fatal("деньги покупателя не теряются из-за ответа сверки")
	}
}

func TestReconcileAppliesStatusWithoutReadingEachOrder(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := paidPurchase(t, e)
	first := state.Groups[0]
	e.platform.SetOrderStatus(first.OrderID, checkout.GroupAssembled, "")
	polls := e.platform.PollCalls(first.TenantID)

	result, err := e.service.ReconcileOrders(ctx)
	if err != nil || result.Mismatched != 1 || groupStatuses(t, e, state.ID)[0] != checkout.GroupAssembled {
		t.Fatalf("статус из ответа пачки: %+v %v", result, err)
	}
	if e.platform.PollCalls(first.TenantID) != polls {
		t.Fatal("статусу без причины и отслеживания отдельный запрос заказа не нужен")
	}
}

func TestResumeSettlesPurchaseWithStaleWorkTime(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := paidPurchase(t, e)
	// Оплаченный заказ работы не имеет; повторный круг не должен ни опрашивать, ни переписывать покупку.
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	settled, err := e.service.Get(ctx, buyerID, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Hour)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	again, err := e.service.Get(ctx, buyerID, state.ID)
	if err != nil || again.Version != settled.Version {
		t.Fatal("покупка без фоновой работы больше не выбирается и не сохраняется", err)
	}
}

func TestReconcileAsksEachShopOnceAndFixesMissedEvent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if result, err := e.service.ReconcileOrders(ctx); err != nil || result.Checked != 0 {
		t.Fatal("без заказов сверять нечего", result, err)
	}
	state := paidPurchase(t, e)
	first, second := state.Groups[0], state.Groups[1]
	e.platform.SetOrderStatus(first.OrderID, checkout.GroupShipped, "")
	polls := e.platform.PollCalls(second.TenantID)

	result, err := e.service.ReconcileOrders(ctx)
	clean := result.Unresolved == 0 && len(result.Failed) == 0
	if err != nil || result.Checked != 2 || result.Mismatched != 1 || !clean {
		t.Fatalf("итог сверки: %+v %v", result, err)
	}
	if groupStatuses(t, e, state.ID)[0] != checkout.GroupShipped {
		t.Fatal("пропущенное событие исправлено сверкой")
	}
	for _, tenant := range []ids.TenantID{first.TenantID, second.TenantID} {
		if calls := e.platform.StatusCalls(tenant); len(calls) != 1 || calls[0] != 1 {
			t.Fatalf("один запрос на магазин: %s %v", tenant, calls)
		}
	}
	if e.platform.PollCalls(second.TenantID) != polls {
		t.Fatal("совпавший заказ не дочитывается")
	}
	if result, err = e.service.ReconcileOrders(ctx); err != nil || result.Mismatched != 0 || result.Checked != 2 {
		t.Fatalf("повторная сверка расхождений не находит: %+v %v", result, err)
	}
}

func TestReconcileIsolatesUnavailableShop(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := paidPurchase(t, e)
	first, second := state.Groups[0], state.Groups[1]
	e.platform.SetOrderStatus(first.OrderID, checkout.GroupAssembled, "")
	e.platform.Fail(second.TenantID, errors.New("нет связи"))

	result, err := e.service.ReconcileOrders(ctx)
	if err != nil || result.Failed[second.TenantID] == nil || result.Mismatched != 1 {
		t.Fatalf("недоступный магазин не задерживает остальные: %+v %v", result, err)
	}
	if groupStatuses(t, e, state.ID) != [2]checkout.GroupStatus{checkout.GroupAssembled, checkout.GroupPaid} {
		t.Fatal("статус недоступного магазина не выдумывается")
	}
}

func TestDeliveredOrderLeavesReconcileAndReturnComesByEvent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := paidPurchase(t, e)
	for _, g := range state.Groups {
		e.platform.SetOrderStatus(g.OrderID, checkout.GroupDelivered, "")
		if err := announceOrder(ctx, e, g.TenantID, state.ID, g.OrderID); err != nil {
			t.Fatal(err)
		}
	}
	result, err := e.service.ReconcileOrders(ctx)
	if err != nil || result.Checked != 0 {
		t.Fatalf("полученный заказ не сверяется: %+v %v", result, err)
	}
	first := state.Groups[0]
	if calls := e.platform.StatusCalls(first.TenantID); len(calls) != 0 {
		t.Fatalf("по полученным заказам магазин не спрашивается: %v", calls)
	}
	e.platform.SetOrderStatus(first.OrderID, checkout.GroupReturned, "")
	if err := announceOrder(ctx, e, first.TenantID, state.ID, first.OrderID); err != nil {
		t.Fatal(err)
	}
	if groupStatuses(t, e, state.ID)[0] != checkout.GroupReturned {
		t.Fatal("возврат после получения приходит событием магазина")
	}
}

// Магазин не уложился в срок чтения на пачке: та же пачка повторяется в этом же проходе после паузы,
// а не откладывается до следующих суток.
func TestReconcileRetriesShopThatMissedReadDeadline(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := paidPurchase(t, e)
	first, second := state.Groups[0], state.Groups[1]
	e.platform.SetOrderStatus(second.OrderID, checkout.GroupShipped, "")
	e.platform.FailNextStatuses(second.TenantID, 2)
	started := e.clock.Now()

	result, err := e.service.ReconcileOrders(ctx)
	if err != nil || len(result.Failed) != 0 || result.Checked != 2 || result.Mismatched != 1 {
		t.Fatalf("магазин сверяется повтором в том же проходе: %+v %v", result, err)
	}
	if groupStatuses(t, e, state.ID)[1] != checkout.GroupShipped {
		t.Fatal("расхождение магазина исправлено в том же проходе")
	}
	if calls := e.platform.StatusCalls(second.TenantID); len(calls) != 3 {
		t.Fatalf("два отказа и успешный повтор: %v", calls)
	}
	if calls := e.platform.StatusCalls(first.TenantID); len(calls) != 1 {
		t.Fatalf("ответивший магазин не переспрашивается: %v", calls)
	}
	if paused := e.clock.Now().Sub(started); paused != 0 {
		t.Fatalf("повтор — отдельным кругом, без пауз в строю: %s", paused)
	}
}

// Магазин, не ответивший и на повторы, пропускается после ограниченного числа попыток.
func TestReconcileGivesUpShopAfterBoundedRetries(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := paidPurchase(t, e)
	second := state.Groups[1]
	e.platform.FailNextStatuses(second.TenantID, 100)
	started := e.clock.Now()

	result, err := e.service.ReconcileOrders(ctx)
	if err != nil || result.Failed[second.TenantID] == nil || result.Checked != 1 {
		t.Fatalf("не ответивший магазин отмечен, остальные сверены: %+v %v", result, err)
	}
	if calls := e.platform.StatusCalls(second.TenantID); len(calls) != 1+appcheckout.ReconcileRetries {
		t.Fatalf("попыток магазину: %v", calls)
	}
	if paused := e.clock.Now().Sub(started); paused != 0 {
		t.Fatalf("повторы — кругами, без пауз в строю: %s", paused)
	}
}

// Отказ размыкателя магазина мгновенный: проход повторяет запрос один раз после срока размыкателя,
// дождавшись его однажды после остальных магазинов, и пропускает магазин, если отказ остался.
func TestReconcileRetriesShopOnceAfterItsBreaker(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := paidPurchase(t, e)
	second := state.Groups[1]
	rejected := &ports.PlatformRejected{NotBefore: e.clock.Now().Add(time.Second), Cause: errBreaker}
	e.platform.Fail(second.TenantID, rejected)
	started := e.clock.Now()

	result, err := e.service.ReconcileOrders(ctx)
	if err != nil || result.Failed[second.TenantID] == nil || result.Checked != 1 {
		t.Fatalf("магазин с открытым размыкателем пропущен, остальные сверены: %+v %v", result, err)
	}
	if calls := e.platform.StatusCalls(second.TenantID); len(calls) != 2 || e.clock.Now().Sub(started) != time.Second {
		t.Fatalf("один повтор после срока размыкателя: %v, %s", calls, e.clock.Now().Sub(started))
	}
}

// Открыт размыкатель общего сервера: каждый магазин получит тот же отказ — проход прерывается.
func TestReconcileStopsWhenServerBreakerIsOpen(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := paidPurchase(t, e)
	rejected := &ports.PlatformRejected{NotBefore: e.clock.Now().Add(time.Second), Cause: errBreaker, Server: true}
	for _, g := range state.Groups {
		e.platform.Fail(g.TenantID, rejected)
	}
	started := e.clock.Now()

	_, err := e.service.ReconcileOrders(ctx)
	if !errors.Is(err, appcheckout.ErrReconcileServerDown) || !e.clock.Now().Equal(started) {
		t.Fatalf("проход прерван без пауз: %v, %s", err, e.clock.Now().Sub(started))
	}
	for _, g := range state.Groups {
		if calls := e.platform.StatusCalls(g.TenantID); len(calls) > 1 {
			t.Fatalf("магазину %s повторяли запрос при открытом размыкателе сервера: %v", g.TenantID, calls)
		}
	}
}
