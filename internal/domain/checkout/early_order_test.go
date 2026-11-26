package checkout_test

import (
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

// earlyCancellation — отмена заказа магазином, о которой площадка узнала раньше ответа создания.
func earlyCancellation(t *testing.T, c *checkout.Checkout, now time.Time) {
	t.Helper()
	early := checkout.SellerOrder{OrderID: "t1-order", Status: checkout.GroupCancelled, Revision: 2}
	if changed, err := c.ApplyOrderState("t1", early, now); err != nil || !changed {
		t.Fatal("раннее состояние сохраняется при группе", changed, err)
	}
}

// Ответ создания сохранился после срока создания: группа уже оставлена, ранняя отмена не подменяет
// её итог — площадка ведёт отмену заказа своим сценарием, раннее состояние снимается.
func TestLateCreationReplyAfterDeadlineKeepsOwnCancellation(t *testing.T) {
	c := creatingWithDeadline(t)
	earlyCancellation(t, c, t0)
	if changed, err := c.ExpireCreation(t0.Add(15 * time.Second)); err != nil || !changed {
		t.Fatal(changed, err)
	}
	attach(t, c, 0)
	g := c.Groups()[0]
	if g.EarlyOrders != nil || !g.ReconciliationRequired || g.Status != checkout.GroupCreationExpired {
		t.Fatalf("поздний ответ после срока: %+v", g)
	}
}

// Ответ создания не пришёл никогда: исход даёт отмена по ключу оформления — нынешнее состояние
// магазина, раннее состояние снимается и не применяется поверх.
func TestEarlyOrderWithoutReplyEndsWithCancellationByKey(t *testing.T) {
	c := creatingWithDeadline(t)
	// Попытка создания ушла магазину, ответ потерян: исход неизвестен до отмены по ключу.
	if _, err := c.ClaimCreation("t1", "attempt", t0.Add(5*time.Second), t0); err != nil {
		t.Fatal(err)
	}
	earlyCancellation(t, c, t0)
	if changed, err := c.ExpireCreation(t0.Add(15 * time.Second)); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if _, err := c.CompleteCancellation("t1", "t1-order", t0.Add(16*time.Second)); err != nil {
		t.Fatal(err)
	}
	g := c.Groups()[0]
	if g.EarlyOrders != nil || !g.CancellationConfirmed || g.OrderID != "t1-order" {
		t.Fatalf("отмена по ключу снимает раннее состояние: %+v", g)
	}
}

func earlyState(order string, status checkout.GroupStatus, revision int64) checkout.SellerOrder {
	return checkout.SellerOrder{OrderID: order, Status: status, Reason: "Заказ отменён магазином", Revision: revision}
}

// Чужое раннее событие со старшей ревизией не вытесняет настоящее: ревизии сравниваются в пределах
// одного номера заказа, и привязка применяет состояние своего заказа без отказа.
func TestEarlyOrderOfOtherOrderDoesNotDisplaceOwn(t *testing.T) {
	c := creatingWithDeadline(t)
	for _, early := range []checkout.SellerOrder{
		earlyState("t1-order", checkout.GroupCancelled, 2),
		earlyState("другой", checkout.GroupCancelled, 9),
	} {
		if _, err := c.ApplyOrderState("t1", early, t0); err != nil {
			t.Fatal(err)
		}
	}
	attach(t, c, 0)
	g := c.Groups()[0]
	if g.Status != checkout.GroupCancelled || g.EarlyOrders != nil || len(c.RejectedEarlyOrders()) != 0 {
		t.Fatalf("настоящая ранняя отмена применена, чужая снята без отказа: %+v %v", g, c.RejectedEarlyOrders())
	}
}

// Повтор того же раннего состояния — не изменение: покупка не переписывается.
func TestEarlyOrderRepeatIsNotChange(t *testing.T) {
	c := creatingWithDeadline(t)
	earlyCancellation(t, c, t0)
	version := c.State().Version
	early := checkout.SellerOrder{OrderID: "t1-order", Status: checkout.GroupCancelled, Revision: 2}
	if changed, err := c.ApplyOrderState("t1", early, t0); err != nil || changed || c.State().Version != version {
		t.Fatal("повтор раннего состояния изменил покупку", changed, err)
	}
}

// Раннее состояние, противоречащее порядку исполнения, при привязке не исчезает молча.
func TestEarlyOrderRejectedOnAttachIsReported(t *testing.T) {
	c := creatingWithDeadline(t)
	// Отмена без причины — такое же событие после привязки отвергается как противоречащее договору.
	cancelled := checkout.SellerOrder{OrderID: "t1-order", Status: checkout.GroupCancelled, Revision: 2}
	if _, err := c.ApplyOrderState("t1", cancelled, t0); err != nil {
		t.Fatal(err)
	}
	attach(t, c, 0)
	rejected := c.RejectedEarlyOrders()
	if len(rejected) != 1 || rejected[0].TenantID != "t1" || rejected[0].Order.OrderID != "t1-order" ||
		rejected[0].Err == nil || c.Groups()[0].EarlyOrders != nil {
		t.Fatalf("отказ раннего состояния отдан сценарию: %+v", rejected)
	}
}

// Группа без начатого создания раннее событие не хранит: покупка не переписывается.
func TestEarlyOrderIgnoredBeforeCreation(t *testing.T) {
	c := ordersReady(t)
	version := c.State().Version
	_, err := c.ApplyOrderState("t1", earlyState("t1-order", checkout.GroupCancelled, 2), t0)
	if !errors.Is(err, checkout.ErrNotFound) || c.State().Version != version || c.Groups()[0].EarlyOrders != nil {
		t.Fatalf("черновик не хранит раннее событие: %v %+v", err, c.Groups()[0])
	}
}

// Отказ отмены по ключу — нынешнее состояние оплаченного заказа: раннее состояние снимается.
func TestBlockedCancellationDropsEarlyOrder(t *testing.T) {
	c := creatingWithDeadline(t)
	if _, err := c.ClaimCreation("t1", "attempt", t0.Add(5*time.Second), t0); err != nil {
		t.Fatal(err)
	}
	earlyCancellation(t, c, t0)
	if _, err := c.ExpireCreation(t0.Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	blocked := t0.Add(16 * time.Second)
	if _, err := c.BlockCancellation("t1", "t1-order", checkout.GroupPaid, "оплачен", blocked); err != nil {
		t.Fatal(err)
	}
	if g := c.Groups()[0]; g.EarlyOrders != nil || g.OrderID != "t1-order" {
		t.Fatalf("отказ отмены снимает раннее состояние: %+v", g)
	}
}

// Ответ с другим пунктом выдачи: группа отклонена, раннее состояние её заказа снято при привязке.
func TestPickupRejectedAttachDropsEarlyOrder(t *testing.T) {
	c := creatingWithDeadline(t)
	earlyCancellation(t, c, t0)
	g := c.Groups()[0]
	total, err := g.Goods.Add(g.DeliveryPrice)
	if err != nil {
		t.Fatal(err)
	}
	order := checkout.SellerOrder{
		Carrier:         c.Delivery().Carrier,
		PickupPointCode: "другой-пункт",
		OrderID:         "t1-order",
		ExpiresAt:       g.ExpiresAt,
		Total:           total,
		Status:          checkout.GroupAwaitingPayment,
	}
	if _, err := c.AttachOrder("t1", order, t0); err != nil {
		t.Fatal(err)
	}
	if g := c.Groups()[0]; g.EarlyOrders != nil || g.Status != checkout.GroupDeliveryUnavailable {
		t.Fatalf("отклонённая привязка снимает раннее состояние: %+v", g)
	}
}

// Отказ соседнего магазина вернул покупку в черновик, а создание этой группы идёт своим ходом:
// раннее событие её заказа хранится, а не теряется.
func TestEarlyOrderHeldWhenNeighbourReturnsPurchaseToDraft(t *testing.T) {
	c := creatingWithDeadline(t)
	if _, err := c.ClaimCreation("t2", "a2", t0.Add(5*time.Second), t0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.FailCreation("t2", "a2", "rejected", "отказ", checkout.GroupRejected, false, nil, t0); err != nil {
		t.Fatal(err)
	}
	if c.Status() != checkout.StatusDraft {
		t.Fatalf("покупка в черновике после отказа соседа: %s", c.Status())
	}
	changed, err := c.ApplyOrderState("t1", earlyState("t1-order", checkout.GroupCancelled, 2), t0)
	if err != nil || !changed || len(c.Groups()[0].EarlyOrders) != 1 {
		t.Fatalf("раннее событие группы с идущим созданием сохранено: %v %v", changed, err)
	}
}

// Оставленная группа (срок создания истёк) раннее событие не хранит: исход даст отмена по ключу.
func TestEarlyOrderIgnoredForAbandonedGroup(t *testing.T) {
	c := creatingWithDeadline(t)
	if _, err := c.ExpireCreation(t0.Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	version := c.State().Version
	_, err := c.ApplyOrderState("t1", earlyState("t1-order", checkout.GroupCancelled, 2), t0.Add(16*time.Second))
	if !errors.Is(err, checkout.ErrNotFound) || c.State().Version != version {
		t.Fatalf("оставленная группа не хранит раннее событие: %v", err)
	}
}

// Пятый номер заказа вытесняет самый давний, не отвергая новое: свежее состояние настоящего заказа
// остаётся, держится не больше четырёх номеров.
func TestEarlyOrderEvictsStalestNumber(t *testing.T) {
	c := creatingWithDeadline(t)
	for _, early := range []checkout.SellerOrder{
		earlyState("t1-order", checkout.GroupCancelled, 2),
		earlyState("чужой-1", checkout.GroupCancelled, 2),
		earlyState("чужой-2", checkout.GroupCancelled, 2),
		earlyState("чужой-3", checkout.GroupCancelled, 2),
		earlyState("t1-order", checkout.GroupCancelled, 3),
		earlyState("чужой-4", checkout.GroupCancelled, 2),
	} {
		if _, err := c.ApplyOrderState("t1", early, t0); err != nil {
			t.Fatal("новое раннее состояние не отвергается", err)
		}
	}
	held := c.Groups()[0].EarlyOrders
	if len(held) != 4 || held[0].OrderID != "чужой-2" {
		t.Fatalf("вытеснен самый давний номер: %+v", held)
	}
	attach(t, c, 0)
	if g := c.Groups()[0]; g.Status != checkout.GroupCancelled {
		t.Fatalf("настоящее раннее состояние применено: %+v", g)
	}
}
