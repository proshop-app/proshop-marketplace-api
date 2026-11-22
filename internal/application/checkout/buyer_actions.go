package checkout

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type shopCommand func(ctx context.Context, orderID string) (checkout.SellerOrder, error)

// CancelPaidOrder отменяет заказ одного магазина до передачи перевозчику; остальные заказы покупки идут дальше.
func (s *Service) CancelPaidOrder(
	ctx context.Context,
	buyerID, id ids.UUID,
	tenant ids.TenantID,
) (checkout.State, error) {
	command := func(ctx context.Context, orderID string) (checkout.SellerOrder, error) {
		return s.platform.CancelPaidOrder(ctx, tenant, orderID)
	}
	return s.buyerAction(ctx, buyerID, id, tenant, checkout.Group.CancellationByBuyer, command)
}

// RequestReturn подаёт заявку на возврат полученного заказа; решение принимает магазин.
func (s *Service) RequestReturn(
	ctx context.Context,
	buyerID, id ids.UUID,
	tenant ids.TenantID,
	reason checkout.ReturnReason,
) (checkout.State, error) {
	if !checkout.ValidReturnReason(reason) {
		return checkout.State{}, checkout.ErrReturnReason
	}
	command := func(ctx context.Context, orderID string) (checkout.SellerOrder, error) {
		return s.platform.RequestReturn(ctx, tenant, orderID, reason)
	}
	return s.buyerAction(ctx, buyerID, id, tenant, checkout.Group.ReturnByBuyer, command)
}

// buyerAction — команда магазину вне транзакции и её состояние тем же правилом, что событие.
// Команда идемпотентна у магазина: потерянный ответ догонит событие или повтор покупателя.
func (s *Service) buyerAction(
	ctx context.Context,
	buyerID, id ids.UUID,
	tenant ids.TenantID,
	decide func(checkout.Group) (bool, error),
	command shopCommand,
) (checkout.State, error) {
	c, err := s.load(ctx, buyerID, id)
	if err != nil {
		return checkout.State{}, err
	}
	var group *checkout.Group
	for _, g := range c.Groups() {
		if g.TenantID == tenant {
			group = &g
			break
		}
	}
	if group == nil {
		return checkout.State{}, checkout.ErrNotFound
	}
	needed, err := decide(*group)
	if err != nil || !needed {
		return c.State(), err
	}
	callCtx, stop := context.WithTimeout(ctx, s.orderCallBudget())
	order, err := command(callCtx, group.OrderID)
	stop()
	if err != nil {
		return checkout.State{}, err
	}
	// Действие у магазина уже состоялось: обрыв запроса покупателя не отменяет его запись.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completionTimeout)
	defer cancel()
	apply := func(c *checkout.Checkout, now time.Time) error {
		_, applyErr := c.ApplyOrderState(tenant, order, now)
		return applyErr
	}
	return s.retrySelection(saveCtx, buyerID, id, nil, apply)
}
