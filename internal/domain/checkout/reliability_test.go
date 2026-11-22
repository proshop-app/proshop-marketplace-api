package checkout_test

import (
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestDeliveryRecheckReturnsToDraftWithNewTotals(t *testing.T) {
	c := ordersReady(t)
	changed, err := c.RecheckDelivery(
		map[ids.TenantID]delivery.Quote{
			"t1": {Status: delivery.QuoteAvailable, MinDays: 7, MaxDays: 9, ExpiresAt: t0.Add(time.Hour)},
			"t2": available(200, t0.Add(time.Hour)),
		},
		t0,
	)
	if err != nil || !changed || c.Status() != checkout.StatusDraft || c.State().DeliveryTotal.Kopecks() != 0 {
		t.Fatalf("новые условия: %+v %v", c.State(), err)
	}
	if _, err := c.BeginOrders(contact, t0); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal(err)
	}
}

func TestOrderAttemptClaimsAndStaleResponses(t *testing.T) {
	c := creatingWithDeadline(t)
	if changed, err := c.ClaimCreation(
		"t1",
		"one",
		t0.Add(5*time.Second+100*time.Millisecond),
		t0,
	); err != nil || !changed {
		t.Fatal(err)
	}
	if changed, err := c.ClaimCreation(
		"t1",
		"two",
		t0.Add(5*time.Second+100*time.Millisecond),
		t0,
	); err != nil || changed {
		t.Fatal("параллельная попытка")
	}
	if changed, err := c.ClaimCreation(
		"t1",
		"two",
		t0.Add(5*time.Second+100*time.Millisecond).Add(5*time.Second+100*time.Millisecond),
		t0.Add(5*time.Second+100*time.Millisecond),
	); err != nil || !changed {
		t.Fatal(err)
	}
	if changed, err := c.FailCreation("t1", "one", "timeout", "timeout", "", false, nil, t0); err != nil || changed {
		t.Fatal("поздний ответ изменил новую попытку")
	}
}

func TestPollBackoffCapAndMonotoneRecovery(t *testing.T) {
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	order := attach(t, c, 0)
	now := order.ExpiresAt.Add(checkout.PaymentExpiryGrace)
	for i := 0; i < checkout.MaxOrderFailures+2; i++ {
		if _, err := c.ClaimPoll("t1", "token", now); err != nil {
			t.Fatal(err)
		}
		if _, err := c.FailPoll("t1", "token", "timeout", now); err != nil {
			t.Fatal(err)
		}
		next := c.Groups()[0].NextPollAt
		if next.Sub(now) > checkout.OrderSlowPoll {
			t.Fatal("нет потолка задержки")
		}
		now = next
	}
	if c.Groups()[0].Status != checkout.GroupSellerUnreachable {
		t.Fatal(c.Groups()[0])
	}
	if _, err := c.ClaimPoll("t1", "recovery", now); err != nil {
		t.Fatal(err)
	}
	order.Status, order.Revision = checkout.GroupShipped, 3
	if _, err := c.CompletePoll("t1", "recovery", order, now); err != nil {
		t.Fatal(err)
	}
	if c.Groups()[0].PollFailures != 0 || c.Groups()[0].Status != checkout.GroupShipped {
		t.Fatal(c.Groups()[0])
	}
	regressed := order
	regressed.Status, regressed.Revision = checkout.GroupAwaitingPayment, 4
	if _, err := c.ApplyOrderState("t1", regressed, now); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal("регресс оплаченного заказа должен быть отклонён")
	}
}

func TestPaidOrderLeavesPollingForEventsAndReconciliation(t *testing.T) {
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	order := attach(t, c, 0)
	if g := c.Groups()[0]; g.PollDue(t0) || !g.ReconcileUntil().IsZero() {
		t.Fatal("неоплаченный заказ ждёт срока и не входит в суточную сверку")
	}
	pending := c.Groups()[0]
	pending.PaymentPending = true
	if !pending.ReconcileUntil().IsZero() {
		t.Fatal("неизвестный исход оплаты не входит в суточную сверку")
	}
	order.Status, order.Revision = checkout.GroupPaid, 2
	if changed, err := c.ApplyOrderState("t1", order, t0); err != nil || !changed {
		t.Fatal(changed, err)
	}
	g := c.Groups()[0]
	if g.PollDue(t0.Add(24*time.Hour)) || !g.ReconcileUntil().Equal(t0.Add(checkout.ReconcileInFlightFor)) {
		t.Fatal("оплаченный заказ не опрашивается и сверяется ограниченный срок от смены статуса")
	}
	if _, found := g.NextWorkAt(); found {
		t.Fatal("у оплаченного заказа нет фоновой работы")
	}
}

func TestLateStateAfterUnnumberedLocalStatusIsStale(t *testing.T) {
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	order := attach(t, c, 0)
	// Статус без номера перехода: так его ставят оплата площадкой и отказ отмены.
	order.Status = checkout.GroupShipped
	if _, err := c.Observe("t1", order, t0); err != nil {
		t.Fatal(err)
	}
	order.Status, order.Revision = checkout.GroupPaid, 2
	if changed, err := c.ApplyOrderState("t1", order, t0); err != nil || changed {
		t.Fatal("опоздавшее событие — не противоречие порядку исполнения", changed, err)
	}
	if c.Groups()[0].Status != checkout.GroupShipped {
		t.Fatal(c.Groups()[0])
	}
}

func TestOrderProgressKeepsShopTimesOnly(t *testing.T) {
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	order := attach(t, c, 0)
	order.Status, order.Revision, order.Number = checkout.GroupPaid, 2, "22112026-0003"
	order.Steps = []checkout.OrderStep{
		{Status: checkout.GroupPaid, At: t0},
		{Status: checkout.GroupSellerUnreachable, At: t0},
		{Status: checkout.GroupAssembled},
	}
	if _, err := c.ApplyOrderState("t1", order, t0); err != nil {
		t.Fatal(err)
	}
	g := c.Groups()[0]
	if g.OrderNumber != "22112026-0003" || len(g.Steps) != 1 || g.Steps[0].Status != checkout.GroupPaid {
		t.Fatalf("в ленту попадают только статусы договора с временем магазина: %+v", g.Steps)
	}
	order.Status, order.Revision, order.Number, order.Steps = checkout.GroupAssembled, 3, "", nil
	if _, err := c.ApplyOrderState("t1", order, t0); err != nil {
		t.Fatal(err)
	}
	if g := c.Groups()[0]; g.OrderNumber == "" || len(g.Steps) != 1 {
		t.Fatalf("состояние без номера и шагов прежние не стирает: %+v", g)
	}
}

func TestSettleWorkTouchesOnlyPurchaseWithoutDueWork(t *testing.T) {
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	attach(t, c, 0)
	// Как после чтения из хранилища: версия меняется только настоящим изменением.
	c = checkout.Restore(c.State())
	version := c.State().Version
	due := c.Groups()[0].ExpiresAt.Add(checkout.PaymentExpiryGrace)
	if err := c.SettleWork(due); err != nil || c.State().Version != version {
		t.Fatal("созревший опрос неоплаченного заказа — работа есть, покупка не трогается", err)
	}
	if err := c.SettleWork(t0.Add(-time.Hour)); err != nil || c.State().Version == version {
		t.Fatal("выбранная без созревшей работы покупка сохраняется, чтобы пересчитать сроки", err)
	}
}

func TestOrderStateFollowsShopRevision(t *testing.T) {
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	order := attach(t, c, 0)
	apply := func(status checkout.GroupStatus, revision int64, at time.Time) (bool, error) {
		order.Status, order.Revision = status, revision
		return c.ApplyOrderState("t1", order, at)
	}
	if changed, err := apply(checkout.GroupAssembled, 5, t0); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if changed, err := apply(checkout.GroupPaid, 3, t0); err != nil || changed {
		t.Fatal("опоздавшее состояние не применяется", changed, err)
	}
	if changed, err := apply(checkout.GroupPaid, 6, t0); err != nil || !changed {
		t.Fatal("магазин вернул собранный заказ в работу", changed, err)
	}
	if _, err := apply(checkout.GroupAwaitingPickup, 9, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(checkout.GroupShipped, 10, t0); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal("из пункта выдачи заказ не возвращается в путь", err)
	}
	delivered := t0.Add(time.Hour)
	if _, err := apply(checkout.GroupDelivered, 11, delivered); err != nil {
		t.Fatal(err)
	}
	g := c.Groups()[0]
	if !g.ReconcileUntil().IsZero() || g.OrderRevision != 11 {
		t.Fatal("полученный заказ не сверяется", g.ReconcileUntil(), g.OrderRevision)
	}
	if _, err := apply(checkout.GroupReturned, 12, delivered); err != nil {
		t.Fatal(err)
	}
	if !c.Groups()[0].ReconcileUntil().IsZero() {
		t.Fatal("возвращённый заказ больше не сверяется")
	}
	other := order
	other.OrderID = "чужой"
	if _, err := c.ApplyOrderState("t1", other, t0); !errors.Is(err, checkout.ErrNotFound) {
		t.Fatal("состояние чужого заказа не относится к группе", err)
	}
}

func TestReturnRemainingIsIsolatedFromSnapshot(t *testing.T) {
	c := ordersReady(t)
	if _, err := c.BeginOrders(contact, t0); err != nil {
		t.Fatal(err)
	}
	order := attach(t, c, 0)
	order.Status, order.Reason = checkout.GroupCancelled, "payment_expired"
	if _, err := c.Observe("t1", order, t0); err != nil {
		t.Fatal(err)
	}
	remaining := []checkout.Line{{Offer: c.Groups()[0].Lines[0].Offer, Quantity: 1, Price: money.FromKopecks(100)}}
	if _, err := c.RecordCartReturn("t1", remaining, "cart.invalid_quantity", t0); err != nil {
		t.Fatal(err)
	}
	state := c.State()
	state.Groups[0].ReturnRemaining[0].Quantity = 99
	if c.Groups()[0].ReturnRemaining[0].Quantity != 1 || c.Groups()[0].CartReturned {
		t.Fatal(c.Groups()[0])
	}
}

func TestLegacyPollDeadlineWaitsForExpiryGrace(t *testing.T) {
	g := checkout.Group{
		Status:     checkout.GroupAwaitingPayment,
		OrderID:    "order",
		ExpiresAt:  t0.Add(time.Hour),
		NextPollAt: t0,
	}
	want := g.ExpiresAt.Add(checkout.PaymentExpiryGrace)
	next, found := g.NextWorkAt()
	if !found || !next.Equal(want) || g.PollDue(want.Add(-time.Nanosecond)) || !g.PollDue(want) {
		t.Fatal("старый срок опроса не обходит срок оплаты с запасом", next, found)
	}
	g.PaymentPending = true
	if next, found = g.NextWorkAt(); !found || !next.Equal(t0) || !g.PollDue(t0) {
		t.Fatal("сохранённый срок неизвестной оплаты не заменяется сроком заказа", next, found)
	}
}
