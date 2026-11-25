package checkout

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (s *Service) enqueueCancellations(ctx context.Context, tx ports.Tx, c *checkout.Checkout) error {
	tasks, err := s.queueCancellations(c)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if err := tx.Outbox().Enqueue(ctx, task); err != nil {
			return err
		}
	}
	return nil
}

// queueCancellations отмечает группы, которым нужна отмена у магазина, и возвращает их задачи.
func (s *Service) queueCancellations(c *checkout.Checkout) ([]ports.NewTask, error) {
	now := s.clock.Now()
	var tasks []ports.NewTask
	for _, g := range c.Groups() {
		if !g.CancellationRequired() || g.CancellationQueued {
			continue
		}
		if err := c.MarkCancellationQueued(g.TenantID, now); err != nil {
			return nil, err
		}
		task, err := newOrderTask(TaskCancelOrder, c.State(), g, now)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func (s *Service) cancelGroup(ctx context.Context, buyerID, id ids.UUID, tenant ids.TenantID) error {
	current, err := s.load(ctx, buyerID, id)
	if err != nil {
		return err
	}
	var group checkout.Group
	for _, g := range current.Groups() {
		if g.TenantID == tenant {
			group = g
			break
		}
	}
	if !group.CancellationRequired() {
		return nil
	}
	reason := "creation_timeout"
	if group.Status == checkout.GroupCancellationRequested {
		reason = "buyer_cancelled"
	}
	started := false
	_, err = s.saveOrderResult(ctx, buyerID, id, tenant, func(c *checkout.Checkout, now time.Time) error {
		var beginErr error
		started, beginErr = c.BeginCancellationAttempt(tenant, now)
		return beginErr
	})
	if err != nil || !started {
		return err
	}
	result, failed := s.platform.CancelOrder(s.orderCallContext(ctx), tenant, ports.CancelOrderRequest{
		ExternalCheckoutID: group.ExternalCheckoutID,
		GroupID:            tenant,
		Reason:             reason,
	})
	// Ответ команды монотонен: позднее создание не должно снять подтверждённый запрет.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completionTimeout)
	defer cancel()
	retry := false
	saved, err := s.saveOrderResult(finishCtx, buyerID, id, tenant, func(c *checkout.Checkout, now time.Time) error {
		retry = false
		var refused *ports.OrderNotCancellable
		if errors.As(failed, &refused) {
			if _, failErr := c.FailCancellation(tenant, "ORDER_NOT_CANCELLABLE", false, now); failErr != nil {
				return failErr
			}
			_, blockErr := c.BlockCancellation(
				tenant,
				refused.OrderID,
				refused.Status,
				"order_not_cancellable:"+string(refused.Status),
				now,
			)
			if blockErr == nil {
				return nil
			}
			if !errors.Is(blockErr, checkout.ErrInvalidOrder) {
				return blockErr
			}
			_, blockErr = c.FailCancellation(tenant, "checkout.cancellation_order_mismatch", true, now)
			return blockErr
		}
		if failed != nil {
			code, transient := cancellationFailure(failed)
			retry = transient
			_, failErr := c.FailCancellation(tenant, code, !transient, now)
			return failErr
		}
		if result.Absent == (result.OrderID != "") {
			_, invalidErr := c.FailCancellation(tenant, "platform.invalid_cancellation_response", true, now)
			return invalidErr
		}
		_, completeErr := c.CompleteCancellation(tenant, result.OrderID, now)
		if errors.Is(completeErr, checkout.ErrInvalidOrder) {
			code := "checkout.cancellation_order_mismatch"
			if result.Absent {
				code = "checkout.cancellation_order_absent"
			}
			_, completeErr = c.FailCancellation(tenant, code, true, now)
		}
		return completeErr
	})
	if err != nil {
		return err
	}
	for _, g := range saved.Groups {
		if g.TenantID == tenant && g.CancellationBlocked {
			slog.Warn(
				"заказ: отмена требует сверки",
				"tenantId",
				tenant,
				"checkoutId",
				id,
				"code",
				g.CancellationLastErrorCode,
				"attempts",
				g.CancellationAttempts,
			)
		}
	}
	if failed != nil {
		slog.Warn("заказ: ошибка отмены", "tenantId", tenant, "checkoutId", id, "retry", retry, "error", failed)
	}
	if retry {
		return failed
	}
	return nil
}

func cancellationFailure(err error) (string, bool) {
	if errors.Is(err, ports.ErrInvalidCancellationResponse) {
		return "platform.invalid_cancellation_response", false
	}
	var refusal *ports.OrderRefusal
	if errors.As(err, &refusal) {
		return refusal.Code, refusal.Code == "MARKETPLACE_TEMPORARY_FAILURE"
	}
	if typed, ok := errs.As(err); ok {
		return typed.Key(), typed.Code() == errs.Unavailable
	}
	var network net.Error
	if errors.As(err, &network) || errors.Is(err, context.Canceled) {
		return "platform.unavailable", true
	}
	return "platform.cancellation_failed", false
}
