package checkout_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type deniedActions struct{}

func TestBuyerCancelsOneShopOrderAndRepeatDoesNotCallShop(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := paidPurchase(t, e)
	first := state.Groups[0]

	cancelled, err := e.service.CancelPaidOrder(ctx, buyerID, state.ID, first.TenantID)
	if err != nil || cancelled.Groups[0].Status != checkout.GroupReturned ||
		cancelled.Groups[0].Reason != checkout.CancelledBeforeShipment {
		t.Fatalf("отмена заказа магазина: %+v %v", cancelled.Groups[0], err)
	}
	if cancelled.Groups[1].Status != checkout.GroupPaid || cancelled.Status != checkout.StatusPaid {
		t.Fatalf("остальные заказы и оплата покупки сохраняются: %+v", cancelled)
	}
	again, err := e.service.CancelPaidOrder(ctx, buyerID, state.ID, first.TenantID)
	if err != nil || again.Version != cancelled.Version || e.platform.ActionCalls(first.TenantID) != 1 {
		t.Fatal("повтор состоявшейся отмены не тревожит магазин", err)
	}
}

func TestBuyerCannotCancelShippedOrForeignOrder(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := paidPurchase(t, e)
	first := state.Groups[0]
	stranger := ids.UUID("11111111-1111-4111-8111-111111111111")
	if _, err := e.service.CancelPaidOrder(ctx, stranger, state.ID, first.TenantID); !errors.Is(
		err,
		checkout.ErrNotFound,
	) {
		t.Fatal("чужую покупку не отменить", err)
	}
	// Магазин уже отдал заказ, событие до площадки ещё не дошло: решает магазин.
	e.platform.SetOrderStatus(first.OrderID, checkout.GroupShipped, "")
	_, err := e.service.CancelPaidOrder(ctx, buyerID, state.ID, first.TenantID)
	if !errors.Is(err, checkout.ErrOrderNotCancellable) {
		t.Fatal("уехавший заказ не отменяется", err)
	}
	if err := announceOrder(ctx, e, first.TenantID, state.ID, first.OrderID); err != nil {
		t.Fatal(err)
	}
	calls := e.platform.ActionCalls(first.TenantID)
	_, err = e.service.CancelPaidOrder(ctx, buyerID, state.ID, first.TenantID)
	if !errors.Is(err, checkout.ErrOrderNotCancellable) || e.platform.ActionCalls(first.TenantID) != calls {
		t.Fatal("известный уехавшим заказ отклоняется без запроса магазину", err)
	}
}

func TestBuyerCancellationKeepsStateWhenShopIsDown(t *testing.T) {
	e := setup(t)
	state := paidPurchase(t, e)
	first := state.Groups[0]
	down := errors.New("нет связи")
	e.platform.Fail(first.TenantID, down)
	_, err := e.service.CancelPaidOrder(context.Background(), buyerID, state.ID, first.TenantID)
	if !errors.Is(err, checkout.ErrOrderActionUnavailable) || !errors.Is(err, down) {
		t.Fatal("покупателю — общий отказ, причина — в журнал", err)
	}
	if typed, ok := errs.As(err); !ok || strings.Contains(typed.Message(), down.Error()) {
		t.Fatal("ответ сервера магазина не показывается покупателю", err)
	}
	if groupStatuses(t, e, state.ID)[0] != checkout.GroupPaid {
		t.Fatal("без ответа магазина отмена не обещается")
	}
}

func TestBuyerRequestsReturnOnceAndSeesRejection(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state := paidPurchase(t, e)
	first := state.Groups[0]
	_, err := e.service.RequestReturn(ctx, buyerID, state.ID, first.TenantID, checkout.ReturnDefective)
	if !errors.Is(err, checkout.ErrReturnUnavailable) {
		t.Fatal("неполученный заказ не возвращают заявкой", err)
	}
	e.platform.SetOrderStatus(first.OrderID, checkout.GroupDelivered, "")
	if err := announceOrder(ctx, e, first.TenantID, state.ID, first.OrderID); err != nil {
		t.Fatal(err)
	}
	_, err = e.service.RequestReturn(ctx, buyerID, state.ID, first.TenantID, "unknown")
	if !errors.Is(err, checkout.ErrReturnReason) {
		t.Fatal("причина — только из словаря", err)
	}
	requested, err := e.service.RequestReturn(ctx, buyerID, state.ID, first.TenantID, checkout.ReturnDefective)
	group := requested.Groups[0]
	if err != nil || group.Status != checkout.GroupDelivered || group.ReturnRequest == nil {
		t.Fatalf("заявка не меняет статус заказа: %+v %v", group, err)
	}
	calls := e.platform.ActionCalls(first.TenantID)
	if _, err := e.service.RequestReturn(ctx, buyerID, state.ID, first.TenantID, checkout.ReturnNotFit); err != nil ||
		e.platform.ActionCalls(first.TenantID) != calls {
		t.Fatal("повтор открытой заявки не создаёт вторую", err)
	}

	order, err := e.platform.GetOrder(ctx, first.TenantID, first.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	rejection := *order.ReturnRequest
	rejection.Status, rejection.Comment = checkout.ReturnRejected, "Следов брака нет"
	order.ReturnRequest, order.Revision = &rejection, order.Revision+1
	if err := applyEvent(ctx, e, first.TenantID, state.ID, order); err != nil {
		t.Fatal(err)
	}
	seen, err := e.service.Get(ctx, buyerID, state.ID)
	if err != nil || seen.Groups[0].ReturnRequest.Comment != "Следов брака нет" {
		t.Fatalf("покупатель видит объяснение магазина: %+v %v", seen.Groups[0].ReturnRequest, err)
	}
	_, err = e.service.RequestReturn(ctx, buyerID, state.ID, first.TenantID, checkout.ReturnDefective)
	if !errors.Is(err, checkout.ErrReturnUnavailable) {
		t.Fatal("решённую заявку повторно не подают", err)
	}
}

func (deniedActions) Allow(context.Context, string, int, time.Duration) (bool, time.Duration, error) {
	return false, time.Minute, nil
}

func TestBuyerActionsAreLimitedBeforeShopCall(t *testing.T) {
	e := setup(t)
	state := paidPurchase(t, e)
	first := state.Groups[0]
	e.service.SetActionLimiter(deniedActions{})
	_, err := e.service.CancelPaidOrder(context.Background(), buyerID, state.ID, first.TenantID)
	if !errors.Is(err, checkout.ErrTooManyActions) || e.platform.ActionCalls(first.TenantID) != 0 {
		t.Fatal("лимит срабатывает до запроса магазину", err)
	}
}
