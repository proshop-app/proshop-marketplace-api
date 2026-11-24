package checkout

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	creationSaveInitialDelay = 20 * time.Millisecond
	creationSaveMaxDelay     = time.Second
	completionTimeout        = 3 * time.Second
)

func (s *Service) createGroup(ctx context.Context, buyerID, id ids.UUID, tenant ids.TenantID) error {
	token := string(s.ids.NewUUID())
	claimed := false
	state, err := s.retrySelection(
		ctx,
		buyerID,
		id,
		nil,
		func(c *checkout.Checkout, now time.Time) error {
			var err error
			claimed, err = c.ClaimCreation(tenant, token, s.creationLease(c, tenant, now), now)
			return err
		},
	)
	if err != nil {
		return err
	}
	if !claimed {
		if state.Status == checkout.StatusDraft || state.Status == checkout.StatusConfirmed {
			return nil
		}
		for _, g := range state.Groups {
			if g.TenantID == tenant && g.OrderID == "" && !g.CreateExhausted &&
				!g.Abandoned && !g.CreationExpired(s.clock.Now()) {
				return errCreationPending
			}
		}
		return nil
	}
	var group checkout.Group
	for _, g := range state.Groups {
		if g.TenantID == tenant {
			group = g
		}
	}
	request := creationRequest(tenant, group, state.Delivery, state.Contact)

	// Повтор команды должен вернуть сохранённый заказ, даже если код уже истёк.
	// Сервер сравнивает снимок discounts внутри транзакции создания.
	var answer checkout.SellerOrder
	failed := s.ready(ctx, []checkout.Group{group})
	if failed == nil {
		// После срока создания заказ закроет задача срока: позже вызывать сервер незачем.
		callCtx := ports.WithPlatformCallDeadline(s.orderCallContext(ctx), group.CreationDeadline)
		answer, failed = s.platform.CreateOrder(callCtx, tenant, request)
	}
	var refusal *ports.OrderRefusal
	if errors.As(failed, &refusal) && refusal.Code == checkout.DispatchOriginChanged {
		return s.reviewDispatch(
			ctx,
			buyerID,
			id,
			group,
			token,
			refusal.DispatchReviewRevision,
			state.Delivery,
		)
	}
	// Полученный orderId сохраняем даже при остановке процесса, но ограничиваем время завершения.
	saveCtx := ctx
	if failed == nil {
		var finish context.CancelFunc
		saveCtx, finish = context.WithTimeout(context.WithoutCancel(ctx), completionTimeout)
		defer finish()
	}
	state, err = s.completeGroupCreation(saveCtx, buyerID, id, group, token, answer, failed)
	if err != nil {
		return err
	}
	for _, g := range state.Groups {
		if g.TenantID == tenant && g.OrderID == "" && !g.CreateExhausted && !g.Abandoned {
			return creationRetry(failed, g)
		}
	}
	return nil
}

// creationRetry откладывает повтор, отклонённый размыкателем, до момента допуска: раньше каждая
// попытка заведомо получила бы тот же отказ. Допуск после срока создания — повторять незачем,
// группу закроет задача срока.
func creationRetry(failed error, group checkout.Group) error {
	var rejected *ports.PlatformRejected
	if !errors.As(failed, &rejected) {
		return errCreationPending
	}
	if !rejected.NotBefore.Before(group.CreationDeadline) {
		return nil
	}
	return &creationPendingUntil{at: rejected.NotBefore}
}

// creationLease покрывает ожидание места в ограждении и сам вызов (каждое — не дольше бюджета),
// но не дальше срока создания: после него ограждение сервер не вызывает и вызов не продолжает.
func (s *Service) creationLease(c *checkout.Checkout, tenant ids.TenantID, now time.Time) time.Time {
	for _, g := range c.Groups() {
		if g.TenantID == tenant {
			return now.Add(min(2*s.orderCallBudget(), g.CreationDeadline.Sub(now)) + creationLeaseSlack)
		}
	}
	return now
}

// Повторяем только временный сбой хранилища и конфликт версии, не маскируя ошибки сценария.
func (s *Service) saveOrderResult(
	ctx context.Context,
	buyerID, id ids.UUID,
	tenant ids.TenantID,
	mutate func(*checkout.Checkout, time.Time) error,
) (checkout.State, error) {
	delay := creationSaveInitialDelay
	for {
		if err := ctx.Err(); err != nil {
			return checkout.State{}, err
		}
		state, err := s.updateSelection(ctx, buyerID, id, s.enqueueCancellations, mutate)
		if err == nil || (!errors.Is(err, checkout.ErrConflict) && errs.CodeOf(err) != errs.Unavailable) {
			return state, err
		}
		slog.Warn("заказ: повтор записи итога", "tenantId", tenant, "checkoutId", id, "delay", delay, "error", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return checkout.State{}, ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, creationSaveMaxDelay)
	}
}

func creationFailure(err error) (string, checkout.GroupStatus, []checkout.PriceChange) {
	if errors.Is(err, checkout.ErrOfferUnavailable) {
		return checkout.ErrOfferUnavailable.Key(), checkout.GroupRejected, nil
	}
	if errors.Is(err, checkout.ErrPromoInvalid) {
		return checkout.ErrPromoInvalid.Key(), checkout.GroupPriceChanged, nil
	}
	var price *ports.PriceChanged
	if errors.As(err, &price) {
		return "PRICE_CHANGED", checkout.GroupPriceChanged, price.Items
	}
	var refusal *ports.OrderRefusal
	if errors.As(err, &refusal) {
		if refusal.Unexpected {
			return refusal.Code, "", nil
		}
		switch refusal.Code {
		case "DELIVERY_UNAVAILABLE", "DELIVERY_CHANGED":
			return refusal.Code, checkout.GroupDeliveryUnavailable, nil
		case "OUT_OF_STOCK", "ORDER_CHECKOUT_CANCELLED", "MARKETPLACE_PAYMENT_DUE_INVALID":
			return refusal.Code, checkout.GroupRejected, nil
		case "IDEMPOTENCY_CONFLICT":
			return refusal.Code, checkout.GroupSellerUnreachable, nil
		default:
			return refusal.Code, "", nil
		}
	}
	if errors.Is(err, ports.ErrMarketplaceDisabled) {
		return "MARKETPLACE_DISABLED", "", nil
	}
	return "seller_unreachable", "", nil
}

func (s *Service) pollGroup(ctx context.Context, buyerID, id ids.UUID, tenant ids.TenantID) error {
	token := string(s.ids.NewUUID())
	claimed := false
	state, err := s.update(ctx, buyerID, id, func(c *checkout.Checkout, now time.Time) error {
		var err error
		claimed, err = c.ClaimPoll(tenant, token, now)
		return err
	})
	if errors.Is(err, checkout.ErrConflict) {
		return nil
	}
	if err != nil || !claimed {
		return err
	}
	orderID := ""
	for _, g := range state.Groups {
		if g.TenantID == tenant {
			orderID = g.OrderID
		}
	}
	answer, failed := s.platform.GetOrder(s.orderCallContext(ctx), tenant, orderID)
	_, err = s.update(ctx, buyerID, id, func(c *checkout.Checkout, now time.Time) error {
		if failed == nil {
			_, observeErr := c.CompletePoll(tenant, token, answer, now)
			if observeErr == nil {
				return nil
			}
			failed = observeErr
		}
		reason := "seller_unreachable"
		var refusal *ports.OrderRefusal
		if errors.As(failed, &refusal) {
			reason = refusal.Code
		}
		if errors.Is(failed, checkout.ErrTransition) || errors.Is(failed, checkout.ErrInvalidOrder) ||
			errors.Is(failed, ports.ErrInvalidOrderStatus) {
			reason = "invalid_status"
		}
		_, err := c.FailPoll(tenant, token, reason, now)
		return err
	})
	return err
}

// orderCallContext передаёт бюджет вызова магазина данными: срок ставит ограждение платформы,
// и зависший магазин засчитывается его размыкателю, а не выглядит отменой вызывающего.
func (s *Service) orderCallContext(ctx context.Context) context.Context {
	return ports.WithPlatformCallBudget(ctx, s.orderCallBudget())
}

func (s *Service) orderCallBudget() time.Duration {
	if s.config.OrderCallTimeout > 0 {
		return min(CreationCallTimeout, s.config.OrderCallTimeout)
	}
	return CreationCallTimeout
}

func creationRequest(
	tenant ids.TenantID, group checkout.Group,
	fallback *checkout.Selection, contact checkout.Contact,
) ports.CreateOrderRequest {
	request := ports.CreateOrderRequest{
		PaymentDueAt:       group.ExpiresAt,
		PromoCode:          group.PromoCode,
		ExternalCheckoutID: group.ExternalCheckoutID,
		GroupID:            tenant,
		Carrier:            string(fallback.Carrier),
		PickupPointCode:    fallback.PickupPointCode,
		Contact:            contact,
		DeliveryPrice:      group.DeliveryPrice,
	}
	for _, line := range group.Lines {
		request.Items = append(
			request.Items,
			ports.OrderItem{
				ProductID: line.Offer.Product,
				Quantity:  line.Quantity,
				Price:     line.Price,
				Discounts: line.Discounts,
			},
		)
	}
	return request
}

func (s *Service) completeGroupCreation(
	ctx context.Context, buyerID, id ids.UUID, group checkout.Group, token string,
	answer checkout.SellerOrder, failed error,
) (checkout.State, error) {
	tenant := group.TenantID
	return s.saveOrderResult(
		ctx,
		buyerID,
		id,
		group.TenantID,
		func(c *checkout.Checkout, now time.Time) error {
			if failed == nil {
				_, attachErr := c.CompleteCreation(tenant, token, answer, now)
				if !errors.Is(attachErr, checkout.ErrInvalidOrder) {
					return attachErr
				}
				failed = attachErr
			}
			code, terminal, prices := creationFailure(failed)
			reason := code
			if errors.Is(failed, checkout.ErrPromoInvalid) {
				reason = checkout.ErrPromoInvalid.Message()
				if typed, ok := errs.As(failed); ok {
					reason = typed.Message()
				}
			}
			var refusal *ports.OrderRefusal
			if errors.As(failed, &refusal) && refusal.Reason != "" {
				reason = refusal.Reason
			}
			// Прежняя попытка могла создать заказ и потерять ответ до отзыва готовности.
			reconcile := code == "IDEMPOTENCY_CONFLICT" || errors.Is(failed, checkout.ErrOfferUnavailable) ||
				(terminal != "" && group.CreateFailures > 0)
			_, err := c.FailCreation(tenant, token, code, reason, terminal, reconcile, prices, now)
			return err
		},
	)
}
