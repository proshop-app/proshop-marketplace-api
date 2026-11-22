package checkout

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/money"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// DispatchOriginChanged — парный код сервера: попытка не создала заказ, нужен новый расчёт и согласие.
const DispatchOriginChanged = "DISPATCH_ORIGIN_CHANGED"

// BeginDispatchReview останавливает создание до расчёта: перезапуск задачи не может тихо создать заказ.
func (c *Checkout) BeginDispatchReview(
	tenant ids.TenantID,
	token string,
	revision int,
	now time.Time,
) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if g.CreateToken != token || g.OrderID != "" || g.Excluded() {
		return false, nil
	}
	g.Status, g.Reason, g.LastErrorCode = GroupDeliveryRequoted, DispatchOriginChanged, DispatchOriginChanged
	g.DispatchReviewRevision = revision
	// Попытка уже была отправлена: явное удаление группы должно закрыть её ключ на сервере.
	g.CreateFailures++
	g.CreateToken = ""
	g.ExpiresAt = time.Time{}
	g.CreationDeadline = time.Time{}
	g.NextCreateAt = time.Time{}
	q := delivery.Unreachable(now)
	g.Quote = &q
	c.state.Status, c.state.ConfirmedAt = StatusDraft, time.Time{}
	return true, c.touch(now)
}

// ApplyDispatchQuote сохраняет ровно один новый расчёт, включая его честный отказ.
func (c *Checkout) ApplyDispatchQuote(tenant ids.TenantID, quote delivery.Quote, now time.Time) error {
	g := c.group(tenant)
	if c.Status() != StatusDraft || g == nil || g.Status != GroupDeliveryRequoted || g.Abandoned {
		return ErrTransition
	}
	g.Quote = &quote
	g.DeliveryPrice = money.Money{}
	if quote.Status == delivery.QuoteUnavailable && quote.Reason != delivery.ReasonSellerUnreachable {
		g.Status = GroupDeliveryUnavailable
		g.ReconciliationRequired = !g.CancellationConfirmed
		g.Abandoned, g.CreateExhausted = true, true
	}
	return c.touch(now)
}
