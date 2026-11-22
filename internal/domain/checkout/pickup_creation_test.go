package checkout_test

import (
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestPickupIsFrozenByCreationStatusOrToken(t *testing.T) {
	for _, status := range []checkout.GroupStatus{"", checkout.GroupCreating, checkout.GroupSellerUnreachable} {
		t.Run(
			string(status),
			func(t *testing.T) {
				c := ordersReady(t)
				state := c.State()
				state.Status = checkout.StatusDraft
				state.Groups[0].Status = status
				if status == "" {
					state.Groups[0].CreateToken = "claimed"
				}
				c = checkout.Restore(state)
				next := *c.Delivery()
				next.PickupPointCode = "OTHER"
				quotes := map[ids.TenantID]delivery.Quote{
					"t1": available(100, t0.Add(time.Hour)),
					"t2": available(100, t0.Add(time.Hour)),
				}
				if err := c.SelectDelivery(next, quotes, t0); !errors.Is(err, checkout.ErrSnapshotChanged) {
					t.Fatal("создание должно замораживать ПВЗ внутри агрегата", err)
				}
			},
		)
	}
}

func TestDifferentOrderPickupRequiresCancellationAndNewConsent(t *testing.T) {
	for _, complete := range []bool{false, true} {
		c := ordersReady(t)
		if _, err := c.BeginOrders(contact, t0); err != nil {
			t.Fatal(err)
		}
		if _, err := c.ClaimCreation("t1", "token", t0.Add(time.Minute), t0); err != nil {
			t.Fatal(err)
		}
		order := checkout.SellerOrder{
			Carrier:         "cdek",
			PickupPointCode: "OTHER",
			OrderID:         "wrong-pickup",
			Total:           c.Groups()[0].Goods,
			ExpiresAt:       t0.Add(checkout.PaymentWindow),
			Status:          checkout.GroupAwaitingPayment,
		}
		var err error
		if complete {
			_, err = c.CompleteCreation("t1", "token", order, t0)
		} else {
			_, err = c.AttachOrder("t1", order, t0)
		}
		group := c.Groups()[0]
		if err != nil || !group.Excluded() || !group.ReconciliationRequired ||
			group.OrderID != order.OrderID || !c.NeedsConfirmation() {
			t.Fatal("несовпадение ПВЗ изолирует заказ до отмены и нового согласия", c.State(), err)
		}
		if _, err := c.PaymentAmount(); err == nil {
			t.Fatal("ошибочный ПВЗ не должен попасть в оплату")
		}
	}
}
