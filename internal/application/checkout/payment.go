package checkout

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Payment проверяет владельца раньше флага, чтобы не раскрывать чужую покупку.
func (s *Service) Payment(ctx context.Context, buyerID, id ids.UUID) (checkout.State, error) {
	state, err := s.Get(ctx, buyerID, id)
	if err != nil {
		return state, err
	}
	if !s.config.PaymentStub {
		return state, checkout.ErrPaymentUnavailable
	}
	if !checkout.Restore(state).PaymentComplete() {
		var awaiting []checkout.Group
		for _, group := range state.Groups {
			if group.EffectiveStatus() == checkout.GroupAwaitingPayment {
				awaiting = append(awaiting, group)
			}
		}
		if err := s.ready(ctx, awaiting); err != nil {
			return state, err
		}
	}
	return state, checkout.Restore(state).CheckPaymentWindow(s.clock.Now())
}

// Pay открывает одну кассу для всех ожидающих групп.
func (s *Service) Pay(ctx context.Context, buyerID, id ids.UUID) (checkout.State, error) {
	state, err := s.Payment(ctx, buyerID, id)
	if err != nil {
		return state, err
	}
	if _, err := checkout.Restore(state).PaymentAmount(); err != nil {
		return state, err
	}
	return state, nil
}

// ConfirmPayment вызывает сервер вне транзакции. После сбоя повтор безопасен:
// сервер помнит успешные платежи; локально сверяется набор подтверждённых заказов.
func (s *Service) ConfirmPayment(ctx context.Context, buyerID, id ids.UUID) error {
	state, err := s.Payment(ctx, buyerID, id)
	if err != nil {
		return err
	}
	if checkout.Restore(state).PaymentComplete() {
		return nil
	}
	c := checkout.Restore(state)
	if _, err := c.PaymentAmount(); err != nil {
		return err
	}
	token := string(s.ids.NewUUID())
	uncertain := false
	state, err = s.retrySelection(ctx, buyerID, id, nil, func(current *checkout.Checkout, now time.Time) error {
		uncertain = current.State().PaymentStarted
		return current.ClaimPayment(token, now)
	})
	if err != nil {
		return err
	}
	unpaid := false
	failed := false
	defer func() {
		if failed && (uncertain || !unpaid) {
			probeCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), completionTimeout)
			unpaid = s.confirmUnpaid(probeCtx, state)
			stop()
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completionTimeout)
		defer cancel()
		finish := func(current *checkout.Checkout, now time.Time) error {
			return current.FinishPayment(token, unpaid, now)
		}
		_, finishErr := s.retrySelection(finishCtx, buyerID, id, nil, finish)
		if finishErr != nil {
			slog.Error("оплата: завершение попытки не сохранено", "checkoutId", id, "error", finishErr)
		}
	}()
	paid := make(map[ids.TenantID]string)
	for _, g := range state.Groups {
		if g.EffectiveStatus() != checkout.GroupAwaitingPayment {
			continue
		}
		amount, err := g.Goods.Add(g.DeliveryPrice)
		if err != nil {
			return err
		}
		if err := s.platform.StubPayment(ctx, g.TenantID, g.OrderID, amount); err != nil {
			failed = true
			slog.Warn("отказ оплаты заказа", "tenantId", g.TenantID, "orderId", g.OrderID, "error", err)
			if errors.Is(err, ports.ErrMarketplaceDisabled) {
				unpaid = len(paid) == 0
				return checkout.ErrTransition.Wrap(err)
			}
			var refusal *ports.OrderRefusal
			if errors.As(err, &refusal) {
				switch refusal.Code {
				case "MARKETPLACE_STUB_PAYMENT_STATE", "MARKETPLACE_STUB_PAYMENT_AMOUNT", "ORDER_NOT_FOUND":
					unpaid = len(paid) == 0 && refusal.Code != "MARKETPLACE_STUB_PAYMENT_STATE"
					return checkout.ErrTransition.Wrap(err)
				}
			}
			return checkout.ErrPaymentUnavailable.Wrap(err)
		}
		paid[g.TenantID] = g.OrderID
	}
	// Повторяем только CAS сохранения; сервер повторно не вызываем.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completionTimeout)
	defer cancel()
	_, err = s.retrySelection(finishCtx, buyerID, id, nil, func(current *checkout.Checkout, now time.Time) error {
		if current.PaymentComplete() {
			return nil
		}
		for _, g := range current.Groups() {
			if g.EffectiveStatus() == checkout.GroupAwaitingPayment && paid[g.TenantID] != g.OrderID {
				return checkout.ErrTransition
			}
		}
		_, err := current.Pay(now)
		return err
	})
	return err
}

// Неопределённая прежняя попытка не считается отказом без сверки всех известных заказов.
func (s *Service) confirmUnpaid(ctx context.Context, state checkout.State) bool {
	for _, g := range state.Groups {
		if g.OrderID == "" {
			continue
		}
		order, err := s.platform.GetOrder(ctx, g.TenantID, g.OrderID)
		if err != nil || order.OrderID != g.OrderID ||
			(order.Status != checkout.GroupAwaitingPayment && order.Status != checkout.GroupCancelled) {
			return false
		}
	}
	return true
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
