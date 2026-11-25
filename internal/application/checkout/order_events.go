package checkout

import (
	"context"
	"errors"
	"log/slog"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// OrderEvent — состояние заказа магазина из его события.
type OrderEvent struct {
	EventID    string
	TenantID   ids.TenantID
	CheckoutID ids.UUID
	Order      checkout.SellerOrder
}

// ErrOrderEventRejected — событие противоречит порядку исполнения заказа: повтор его не исправит.
var ErrOrderEventRejected = errs.New(
	errs.Invalid,
	"checkout.order_event_rejected",
	"состояние заказа магазина не подходит покупке",
)

// ApplyOrderEvent применяет состояние заказа вместе с отметкой события: повтор доставки ничего не меняет.
func (s *Service) ApplyOrderEvent(ctx context.Context, event OrderEvent) error {
	var purchases []ports.NewTask
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		fresh, err := tx.Inbox().MarkProcessed(ctx, event.EventID, s.clock.Now())
		if err != nil || !fresh {
			return err
		}
		_, err = s.applyOrderState(ctx, tx, event.CheckoutID, event.TenantID, event.Order, &purchases)
		return err
	})
	s.enqueueSearchPurchases(ctx, purchases)
	return err
}

// applyOrderState сохраняет состояние заказа из события или сверки и сообщает, изменилась ли покупка.
// Покупки или заказа у площадки может не быть — заказ создан другой установкой: это не сбой.
// Ответ создания, ещё не сохранённый к приходу события, — временная ошибка: приём повторит событие.
func (s *Service) applyOrderState(
	ctx context.Context,
	tx ports.Tx,
	checkoutID ids.UUID,
	tenant ids.TenantID,
	order checkout.SellerOrder,
	purchases *[]ports.NewTask,
) (bool, error) {
	// Под блокировкой строки: события разных магазинов одной покупки приходят из разных партиций
	// и реплик и ждут друг друга, а не уходят в повтор приёма из-за конфликта версии.
	c, err := tx.Checkouts().GetForUpdate(ctx, checkoutID)
	if err != nil {
		return false, orderStateFailure(err, checkoutID, tenant, order)
	}
	previous := c.State()
	changed, err := c.ApplyOrderState(tenant, order, s.clock.Now())
	if err != nil || !changed {
		return false, orderStateFailure(err, checkoutID, tenant, order)
	}
	if err := tx.Checkouts().Update(ctx, c); err != nil {
		return false, err
	}
	s.observeTransitions(tx, previous, c.State())
	s.collectSearchPurchases(ctx, tx, previous, c.State(), purchases)
	return true, nil
}

func orderStateFailure(err error, checkoutID ids.UUID, tenant ids.TenantID, order checkout.SellerOrder) error {
	switch {
	case errors.Is(err, checkout.ErrNotFound):
		slog.Warn(
			"заказ магазина не относится к покупке площадки",
			slog.String("tenantId", string(tenant)),
			slog.String("checkoutId", string(checkoutID)),
			slog.String("orderId", order.OrderID),
		)
		return nil
	case errors.Is(err, checkout.ErrTransition), errors.Is(err, checkout.ErrInvalidOrder):
		return ErrOrderEventRejected.Wrap(err)
	}
	return err
}
