package checkout

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

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
		if !g.ExpiresAt.IsZero() && !g.ExpiresAt.After(now) {
			g.PaymentHold = HoldPaymentExpired
		} else if PaymentHoldReason(reason) {
			g.PaymentHold = reason
		}
	}
	return state
}
