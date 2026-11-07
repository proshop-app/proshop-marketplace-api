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
			slog.Warn("отказ оплаты заказа", "tenantId", g.TenantID, "orderId", g.OrderID, "error", err)
			if errors.Is(err, ports.ErrMarketplaceDisabled) {
				return checkout.ErrTransition.Wrap(err)
			}
			var refusal *ports.OrderRefusal
			if errors.As(err, &refusal) {
				switch refusal.Code {
				case "MARKETPLACE_STUB_PAYMENT_STATE", "MARKETPLACE_STUB_PAYMENT_AMOUNT", "ORDER_NOT_FOUND":
					return checkout.ErrTransition.Wrap(err)
				}
			}
			return checkout.ErrPaymentUnavailable.Wrap(err)
		}
		paid[g.TenantID] = g.OrderID
	}
	// Короткую транзакцию повторяем при гонке с опросом; сервер повторно не вызываем.
	for range 3 {
		_, err = s.update(ctx, buyerID, id, func(current *checkout.Checkout, now time.Time) error {
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
		if !errors.Is(err, checkout.ErrConflict) {
			return err
		}
	}
	return err
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
