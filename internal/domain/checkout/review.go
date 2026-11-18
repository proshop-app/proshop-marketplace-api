package checkout

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Excluded отличает окончательно снятую группу от временно недоступного продавца.
func (g Group) Excluded() bool {
	return !g.Paid() && (g.Abandoned || g.CreateExhausted || g.Returnable() ||
		(g.Quote != nil && g.Quote.Status == delivery.QuoteUnavailable &&
			g.Quote.Reason != delivery.ReasonSellerUnreachable))
}

// QuotePending требует повтора, сохраняя группу в подтверждаемом составе.
func (g Group) QuotePending() bool {
	return g.Quote != nil && g.Quote.Status == delivery.QuoteUnavailable &&
		g.Quote.Reason == delivery.ReasonSellerUnreachable
}

// ReviewGroups снимает окончательно недоступные группы и требует нового согласия.
// Уже созданные заказы остальных продавцов сохраняются с прежними ключами.
func (c *Checkout) ReviewGroups(reasons map[ids.TenantID]string, now time.Time) (bool, error) {
	if c.state.PaymentStarted {
		return false, ErrTransition
	}
	changed := false
	for i := range c.state.Groups {
		g := &c.state.Groups[i]
		reason, found := reasons[g.TenantID]
		if !found || g.Excluded() || g.Paid() || PaymentHoldReason(reason) {
			continue
		}
		g.Quote = &delivery.Quote{Status: delivery.QuoteUnavailable, Reason: reason, ExpiresAt: now}
		g.Reason, g.LastErrorCode = reason, reason
		if c.state.CartConsumed {
			g.ReconciliationRequired = g.OrderID != "" || g.CreateToken != "" || g.CreateFailures > 0
			g.Abandoned, g.CreateExhausted = true, true
			g.Status = GroupDeliveryUnavailable
		}
		changed = true
	}
	if !changed {
		return false, nil
	}
	c.reviewComposition()
	return true, c.touch(now)
}

// DeliveryChanged сравнивает обещание покупателю; продление TTL само по себе не меняет его.
func (g Group) DeliveryChanged(q delivery.Quote) bool {
	if g.Quote == nil {
		return q.Status != delivery.QuoteAvailable
	}
	old := g.Quote
	return old.Status != q.Status ||
		old.MinDays != q.MinDays || old.MaxDays != q.MaxDays
}
