package checkout

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Payment проверяет владельца раньше флага, чтобы не раскрывать чужую покупку.
func (s *Service) Payment(ctx context.Context, buyerID, id ids.UUID) (checkout.State, error) {
	c, err := s.paymentCheckout(ctx, buyerID, id)
	if err != nil {
		if c != nil {
			return c.State(), err
		}
		return checkout.State{}, err
	}
	state, err := s.paymentView(ctx, c)
	if err == nil && !c.PaymentComplete() {
		_, err = checkout.Restore(state).PaymentAmount()
	}
	return state, err
}

func (s *Service) paymentCheckout(ctx context.Context, buyerID, id ids.UUID) (*checkout.Checkout, error) {
	c, err := s.load(ctx, buyerID, id)
	if err != nil {
		return nil, err
	}
	if !s.config.PaymentStub {
		return c, checkout.ErrPaymentUnavailable
	}
	return c, nil
}

func (s *Service) preparePayment(ctx context.Context, buyerID, id ids.UUID) (checkout.State, error) {
	c, err := s.paymentCheckout(ctx, buyerID, id)
	if err != nil {
		if c != nil {
			return c.State(), err
		}
		return checkout.State{}, err
	}
	if c.PaymentComplete() {
		return c.State(), nil
	}
	reviewed, changed, err := s.reviewReadiness(ctx, c)
	if err != nil {
		return reviewed, err
	}
	if changed {
		return reviewed, checkout.ErrDeliveryChanged
	}
	return reviewed, nil
}

// Pay открывает одну кассу для всех ожидающих групп.
func (s *Service) Pay(ctx context.Context, buyerID, id ids.UUID) (checkout.State, error) {
	state, err := s.preparePayment(ctx, buyerID, id)
	if err != nil {
		return state, err
	}
	if _, err := checkout.Restore(state).PaymentAmount(); err != nil {
		return state, err
	}
	state, err = s.holdPayment(ctx, state)
	if err != nil {
		return state, err
	}
	if !checkout.Restore(state).PaymentComplete() {
		_, err = checkout.Restore(state).PaymentAmount()
	}
	return state, err
}

// ConfirmPayment отправляет только доступные группы; исходы сохраняются даже при частичном отказе.
func (s *Service) ConfirmPayment(ctx context.Context, buyerID, id ids.UUID) error {
	state, err := s.preparePayment(ctx, buyerID, id)
	if err != nil {
		return paymentFailure(state, err)
	}
	c := checkout.Restore(state)
	if c.PaymentComplete() {
		return nil
	}
	if _, err := c.PaymentAmount(); err != nil {
		return paymentFailure(state, err)
	}
	attempt := paymentAttempt{
		token:     string(s.ids.NewUUID()),
		orders:    c.PaymentOrders(),
		previous:  make(map[ids.TenantID]bool),
		attempted: make(map[ids.TenantID]bool),
		result:    checkout.PaymentResult{Paid: make(map[ids.TenantID]string), Unpaid: make(map[ids.TenantID]bool)},
	}
	claimed, err := s.retrySelection(
		ctx,
		buyerID,
		id,
		nil,
		func(current *checkout.Checkout, now time.Time) error {
			for _, group := range current.Groups() {
				attempt.previous[group.TenantID] = group.PaymentPending
			}
			return current.ClaimPayment(attempt.token, attempt.orders, now)
		},
	)
	if err != nil {
		return paymentFailure(state, err)
	}
	err = s.chargePaymentGroups(ctx, claimed, &attempt)
	saved, finishErr := s.finishPayment(ctx, buyerID, id, &attempt)
	if finishErr != nil {
		return paymentFailure(claimed, finishErr)
	}
	return paymentFailure(checkout.Restore(saved).PaymentView(paymentHolds(state), s.clock.Now()), err)
}

func (s *Service) chargePaymentGroups(ctx context.Context, state checkout.State, attempt *paymentAttempt) error {
	for _, g := range state.Groups {
		if attempt.orders[g.TenantID] != g.OrderID || g.OrderID == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return checkout.ErrPaymentUnavailable.Wrap(err)
		}
		attempt.attempted[g.TenantID] = true
		amount, err := g.Goods.Add(g.DeliveryPrice)
		if err != nil {
			return err
		}
		err = s.platform.StubPayment(s.orderCallContext(ctx), g.TenantID, g.OrderID, amount)
		if err != nil {
			slog.Warn("отказ оплаты заказа", "tenantId", g.TenantID, "orderId", g.OrderID, "error", err)
			return paymentRefusal(g.TenantID, err, attempt)
		}
		attempt.result.Paid[g.TenantID] = g.OrderID
	}
	return nil
}

func (s *Service) finishPayment(
	ctx context.Context, buyerID, id ids.UUID, attempt *paymentAttempt,
) (checkout.State, error) {
	for tenant := range attempt.orders {
		if !attempt.attempted[tenant] && !attempt.previous[tenant] {
			attempt.result.Unpaid[tenant] = true
		}
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completionTimeout)
	defer cancel()
	return s.retrySelection(
		finishCtx,
		buyerID,
		id,
		nil,
		func(current *checkout.Checkout, now time.Time) error {
			return current.FinishPayment(attempt.token, attempt.result, now)
		},
	)
}

// PaymentReturnURL — фиксированный адрес витрины, параметры браузера не используются.
func (s *Service) PaymentReturnURL(id ids.UUID) string {
	return strings.TrimRight(s.config.ReturnBaseURL, "/") + "/checkout/" + string(id) + "/return"
}

// PaymentAmount позволяет странице показывать остаток, а повтору — оплаченную покупку.
func PaymentAmount(state checkout.State) (money.Money, error) {
	if checkout.Restore(state).PaymentComplete() {
		return money.Money{}, nil
	}
	return checkout.Restore(state).PaymentAmount()
}
