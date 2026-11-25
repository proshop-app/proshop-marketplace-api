package checkout_test

import (
	"context"
	"testing"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// Ответ создания потерян, магазин успел отменить заказ: событие отмены приходит раньше, чем
// площадка узнала номер заказа. Оно должно дождаться ответа повтором, а не пропасть до опроса.
func TestOrderEventBeforeCreationReplyWaitsForReply(t *testing.T) {
	e := setup(t)
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
	event := appcheckout.OrderEvent{
		EventID:    "orders:0:7",
		TenantID:   task.TenantID,
		CheckoutID: c.ID,
		Order: checkout.SellerOrder{
			OrderID:  "order-1",
			Status:   checkout.GroupCancelled,
			Reason:   "Заказ отменён магазином",
			Revision: 2,
		},
	}
	err := e.service.ApplyOrderEvent(ctx, event)
	if errs.RetryKindOf(err) != errs.RetryTransient {
		t.Fatal("событие раньше ответа создания повторяется, а не подтверждается молча", err)
	}
	e.clock.Advance(checkout.CreationRetryDelay)
	if err := e.service.HandleOrderTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := e.service.ApplyOrderEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].Status != checkout.GroupCancelled {
		t.Fatalf("повтор события после ответа создания применяет отмену: %v %+v", err, state.Groups[0])
	}
}

// Группа, оставленная без заказа после срока создания, события не ждёт: исход узнаёт отмена по ключу.
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
	err = e.service.ApplyOrderEvent(ctx, appcheckout.OrderEvent{
		EventID:    "orders:0:8",
		TenantID:   state.Groups[0].TenantID,
		CheckoutID: c.ID,
		Order:      checkout.SellerOrder{OrderID: "order-1", Status: checkout.GroupCancelled, Revision: 2},
	})
	if err != nil {
		t.Fatal("после срока создания событие не держит приём", err)
	}
}
