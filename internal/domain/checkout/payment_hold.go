package checkout

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// PaymentWindow задаёт единый срок оплаты покупки, чтобы он не зависел от настроек магазинов.
const PaymentWindow = 10 * time.Minute

// Причины ожидания товара не являются окончательным отказом магазина.
const (
	HoldModerationPending = "moderation_pending"
	HoldPublicationPaused = "publication_paused"
	HoldPaymentExpired    = "payment_expired"
)

// PaymentHoldReason отличает временную проверку товара от окончательного отказа.
func PaymentHoldReason(reason string) bool {
	return reason == HoldModerationPending || reason == HoldPublicationPaused
}

// PaymentView дополняет копию актуальными причинами, не меняя покупку или её версию.
func (c *Checkout) PaymentView(reasons map[ids.TenantID]string, now time.Time) State {
	state := c.State()
	for i := range state.Groups {
		g := &state.Groups[i]
		g.PaymentHold = ""
		reason := reasons[g.TenantID]
		if g.OrderID == "" || g.EffectiveStatus() != GroupAwaitingPayment {
			continue
		}
		if !g.PaymentDeadline().IsZero() && !g.PaymentDeadline().After(now) {
			g.PaymentHold = HoldPaymentExpired
		} else if PaymentHoldReason(reason) {
			g.PaymentHold = reason
		}
	}
	return state
}

// PaymentDeadline возвращает удержание, если оно есть, иначе исходный срок.
// Сервер гарантирует, что удержание не сокращает исходный срок оплаты.
func (g Group) PaymentDeadline() time.Time {
	if !g.PaymentHeldUntil.IsZero() {
		return g.PaymentHeldUntil
	}
	return g.ExpiresAt
}

// PaymentHoldConfirmed принимает удержание ожидающего заказа или уже принятую оплату.
func (o SellerOrder) PaymentHoldConfirmed() bool {
	return (o.Status == GroupAwaitingPayment && !o.PaymentHeldUntil.IsZero()) || paidStatus(o.Status)
}

// RecordPaymentHolds сохраняет все подтверждения одной записью покупки.
func (c *Checkout) RecordPaymentHolds(orders map[ids.TenantID]SellerOrder, now time.Time) error {
	for tenant, order := range orders {
		g := c.group(tenant)
		if g == nil || g.OrderID != order.OrderID || !order.PaymentHoldConfirmed() {
			continue
		}
		// Конкурентное событие отмены или оплаты уже могло сделать ответ команды устаревшим.
		if g.EffectiveStatus() != GroupAwaitingPayment || order.Revision < g.OrderRevision {
			continue
		}
		if _, err := c.ApplyOrderState(tenant, order, now); err != nil {
			return err
		}
	}
	return nil
}
