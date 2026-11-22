package checkout

import (
	"context"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type holdResult struct {
	tenant ids.TenantID
	order  checkout.SellerOrder
	err    error
}

func (s *Service) holdPayment(ctx context.Context, state checkout.State) (checkout.State, error) {
	held, failed := s.requestPaymentHolds(ctx, state.Groups)
	if len(held) == 0 {
		return state, failed
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completionTimeout)
	defer cancel()
	saved, err := s.retrySelection(
		finishCtx,
		state.BuyerID,
		state.ID,
		nil,
		func(current *checkout.Checkout, now time.Time) error {
			return current.RecordPaymentHolds(held, now)
		},
	)
	if err != nil {
		return state, err
	}
	saved = checkout.Restore(saved).PaymentView(paymentHolds(state), s.clock.Now())
	if failed != nil {
		return saved, failed
	}
	return saved, heldPaymentReady(state, saved, s.clock.Now())
}

func (s *Service) requestPaymentHolds(
	ctx context.Context,
	groups []checkout.Group,
) (map[ids.TenantID]checkout.SellerOrder, error) {
	results := make(chan holdResult, len(groups))
	pending := 0
	for _, group := range groups {
		if !group.ReadyForPayment() || !group.PaymentHeldUntil.IsZero() {
			continue
		}
		pending++
		go func() {
			results <- s.holdPaymentGroup(ctx, group)
		}()
	}
	held := make(map[ids.TenantID]checkout.SellerOrder)
	var failed error
	for range pending {
		result := <-results
		if result.err != nil {
			if failed == nil || errors.Is(result.err, checkout.ErrPaymentExpired) {
				failed = result.err
			}
		} else {
			held[result.tenant] = result.order
		}
	}
	return held, failed
}

func (s *Service) holdPaymentGroup(ctx context.Context, group checkout.Group) holdResult {
	// Срок вызова задаёт только ограждение платформы: свой равный срок истекал бы первым,
	// и размыкатель не засчитывал бы зависший магазин.
	order, err := s.platform.PaymentHold(ctx, group.TenantID, group.OrderID)
	result := holdResult{tenant: group.TenantID, order: order}
	if err != nil {
		var refusal *ports.OrderRefusal
		if errors.As(err, &refusal) && refusal.Code == "ORDER_PAYMENT_EXPIRED" {
			result.err = checkout.ErrPaymentExpired.Wrap(err)
		} else {
			result.err = checkout.ErrPaymentUnavailable.Wrap(err)
		}
	} else if order.OrderID != group.OrderID || !order.PaymentHoldConfirmed() {
		result.err = checkout.ErrPaymentUnavailable
	}
	return result
}

func heldPaymentReady(before, after checkout.State, now time.Time) error {
	orders := checkout.Restore(before).PaymentOrders()
	for _, group := range after.Groups {
		if orders[group.TenantID] != group.OrderID || group.Paid() {
			continue
		}
		if !now.Before(group.PaymentDeadline()) {
			return checkout.ErrPaymentExpired
		}
		if !group.ReadyForPayment() || group.PaymentHeldUntil.IsZero() {
			return checkout.ErrPaymentUnavailable
		}
	}
	return nil
}
