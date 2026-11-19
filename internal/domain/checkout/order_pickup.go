package checkout

import "time"

// Неоплаченный заказ на другой ПВЗ изолируется до cancel-by-checkout и нового согласия.
// Ключ группы сохраняется: автоматическое пересоздание с другим телом запрещено.
func (c *Checkout) rejectOrderPickup(g *Group, order SellerOrder, now time.Time) (bool, error) {
	g.OrderID, g.ExpiresAt = order.OrderID, order.ExpiresAt
	if !g.CancellationConfirmed {
		g.LastOrderStatus = GroupAwaitingPayment
	}
	g.Abandoned, g.CreateExhausted = true, true
	g.Status, g.Reason = GroupDeliveryUnavailable, "Пункт выдачи заказа изменился. Проверьте состав покупки"
	g.Quote = nil
	g.ReconciliationRequired = !g.CancellationConfirmed
	g.NextPollAt = now
	c.reviewComposition()
	return true, c.touch(now)
}
