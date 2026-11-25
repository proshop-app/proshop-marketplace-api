package checkout_test

import (
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
	if g.EarlyOrder != nil || !g.ReconciliationRequired || g.Status != checkout.GroupCreationExpired {
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
	if g.EarlyOrder != nil || !g.CancellationConfirmed || g.OrderID != "t1-order" {
		t.Fatalf("отмена по ключу снимает раннее состояние: %+v", g)
	}
}

// Состояние другого заказа при привязке отбрасывается.
func TestEarlyOrderOfOtherOrderIsDropped(t *testing.T) {
	c := creatingWithDeadline(t)
	other := checkout.SellerOrder{OrderID: "другой", Status: checkout.GroupCancelled, Revision: 2}
	if _, err := c.ApplyOrderState("t1", other, t0); err != nil {
		t.Fatal(err)
	}
	attach(t, c, 0)
	g := c.Groups()[0]
	if g.EarlyOrder != nil || g.Status != checkout.GroupAwaitingPayment {
		t.Fatalf("чужое раннее состояние не применяется: %+v", g)
	}
}
