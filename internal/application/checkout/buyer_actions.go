package checkout

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Лимит команд магазину на покупателя: нажатия не должны занимать места оформления магазина.
const (
	buyerActionLimit  = 10
	buyerActionWindow = time.Minute
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
	c, err := s.LoadForBuyer(ctx, buyerID, id)
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
	if err != nil {
		return checkout.State{}, err
	}
	if !needed {
		return s.paymentView(ctx, c)
	}
	if err := s.allowAction(ctx, buyerID); err != nil {
		return checkout.State{}, err
	}
	// Срок вызова задаёт только ограждение платформы: свой равный срок истекал бы первым,
	// и размыкатель не засчитывал бы зависший магазин.
	order, err := command(ctx, group.OrderID)
	if err != nil {
		return checkout.State{}, actionFailure(err, id, tenant)
	}
	// Действие у магазина уже состоялось: обрыв запроса покупателя не отменяет его запись.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completionTimeout)
	defer cancel()
	apply := func(c *checkout.Checkout, now time.Time) error {
		_, applyErr := c.ApplyOrderState(tenant, order, now)
		return applyErr
	}
	state, err := s.retrySelection(saveCtx, buyerID, id, nil, apply)
	if err != nil {
		return state, err
	}
	return s.paymentView(ctx, checkout.Restore(state))
}

// SetActionLimiter подключает лимит команд покупателя; общий на все реплики — в Redis.
func (s *Service) SetActionLimiter(limiter ports.RateLimiter) { s.actions = limiter }

func (s *Service) allowAction(ctx context.Context, buyerID ids.UUID) error {
	if s.actions == nil {
		return nil
	}
	key := "order-action:" + string(buyerID)
	allowed, _, err := s.actions.Allow(ctx, key, buyerActionLimit, buyerActionWindow)
	if err != nil {
		return err
	}
	if !allowed {
		return checkout.ErrTooManyActions
	}
	return nil
}

// actionFailure отдаёт покупателю только отказы договора: ответ сервера магазина с адресами и телом — в журнал.
func actionFailure(err error, id ids.UUID, tenant ids.TenantID) error {
	switch {
	case errors.Is(err, checkout.ErrOrderNotCancellable):
		return checkout.ErrOrderNotCancellable
	case errors.Is(err, checkout.ErrReturnUnavailable):
		return checkout.ErrReturnUnavailable
	}
	slog.Warn(
		"действие покупателя: магазин не подтвердил",
		slog.String("tenantId", string(tenant)),
		slog.String("checkoutId", string(id)),
		slog.Any("error", err),
	)
	return checkout.ErrOrderActionUnavailable.Wrap(err)
}
