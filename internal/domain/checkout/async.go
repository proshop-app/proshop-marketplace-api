package checkout

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CreationResult — результат создания, независимо от дальнейшего исполнения.
type CreationResult uint8

// Итоги создания отделены от оплаты, доставки и отмены заказа.
const (
	CreationNotStarted CreationResult = iota
	CreationPending
	CreationCreated
	CreationUnconfirmed
	CreationRejected
)

// SetCreationDeadline сохраняет единый срок вместе с намерением, до публикации задач.
func (c *Checkout) SetCreationDeadline(deadline, now time.Time) error {
	if c.CreationComplete() {
		return nil
	}
	if c.Status() != StatusCreatingOrders || !deadline.After(now) {
		return ErrTransition
	}
	for i := range c.state.Groups {
		if c.state.Groups[i].OrderID == "" && !c.state.Groups[i].Excluded() {
			c.state.Groups[i].CreationDeadline = deadline
		}
	}
	return c.touch(now)
}

// CreationExpired отмечает достижение срока; закрытие отдельно учитывает активную аренду попытки.
func (g Group) CreationExpired(now time.Time) bool {
	return !g.CreationDeadline.IsZero() && !now.Before(g.CreationDeadline) &&
		!g.Abandoned && !g.CreateExhausted && g.OrderID == "" && g.Status != GroupCreationExpired
}

// ExpireCreation отделяет итог для покупателя от незавершённой сверки магазина.
func (c *Checkout) ExpireCreation(now time.Time) (bool, error) {
	changed := false
	for i := range c.state.Groups {
		g := &c.state.Groups[i]
		if !g.CreationExpired(now) || (g.CreateToken != "" && now.Before(g.NextCreateAt)) {
			continue
		}
		// Истечение бюджета не доказывает отказ магазина: состав и корзина сохраняются.
		g.Status, g.Reason = GroupCreationExpired, "creation_timeout"
		g.ReconciliationRequired = g.CreateToken != "" || g.CreateFailures > 0
		changed = true
	}
	if !changed {
		return false, nil
	}
	c.reviewComposition()
	return true, c.touch(now)
}

// CreationComplete — все группы получили результат, даже если магазин ещё требует сверки.
func (c *Checkout) CreationComplete() bool {
	if c.Status() == StatusDraft || c.Status() == StatusConfirmed {
		return false
	}
	for _, g := range c.state.Groups {
		if g.EffectiveStatus() == GroupCreating || g.EffectiveStatus() == GroupSellerUnreachable {
			return false
		}
	}
	return true
}

// CreationResult отделяет результат создания от дальнейших статусов исполнения заказа.
func (g Group) CreationResult() CreationResult {
	if g.Status == "" {
		return CreationNotStarted
	}
	if g.Status == GroupUnconfirmed {
		return CreationUnconfirmed
	}
	if g.OrderID != "" {
		return CreationCreated
	}
	if g.Status == GroupCancellationRequested && g.ReconciliationRequired {
		return CreationUnconfirmed
	}
	if g.Returnable() || g.Paid() {
		return CreationRejected
	}
	return CreationPending
}

// HasOrders исключает из кабинета намерения, для которых ни один заказ не был создан.
func (c *Checkout) HasOrders() bool {
	for _, g := range c.state.Groups {
		if g.OrderID != "" {
			return true
		}
	}
	return false
}

// Cancel запрещает оплату немедленно. Подтверждение отмены магазина хранится отдельно.
func (c *Checkout) Cancel(now time.Time) (bool, error) {
	if c.state.PaymentStarted {
		return false, ErrTransition
	}
	if (c.Status() == StatusDraft || c.Status() == StatusConfirmed) && !c.state.CartConsumed {
		return false, ErrTransition
	}
	changed := false
	for i := range c.state.Groups {
		g := &c.state.Groups[i]
		if g.Returnable() || g.Paid() {
			continue
		}
		g.ReconciliationRequired = g.OrderID != "" || g.CreateToken != "" || g.CreateFailures > 0
		g.Abandoned, g.CreateExhausted = true, true
		g.Status, g.Reason = GroupCancellationRequested, "buyer_cancelled"
		if !g.ReconciliationRequired {
			g.Status = GroupRejected
		}
		changed = true
	}
	if !changed && c.Status() != StatusDraft && c.Status() != StatusConfirmed {
		return false, nil
	}
	if !c.HasPaidGroups() {
		c.state.Status = StatusCreatingOrders
	}
	c.rollup()
	return true, c.touch(now)
}

// ClaimPayment ограждает отмену и фиксирует конкретные заказы до внешнего вызова.
func (c *Checkout) ClaimPayment(token string, orders map[ids.TenantID]string, now time.Time) error {
	if c.NeedsConfirmation() {
		return ErrDeliveryChanged
	}
	if c.Status() != StatusAwaitingPayment && c.Status() != StatusPartiallyPaid {
		return ErrTransition
	}
	if len(orders) == 0 {
		return ErrTransition
	}
	for tenant, orderID := range orders {
		g := c.group(tenant)
		if g == nil || g.Paid() || g.Excluded() || g.OrderID != orderID || g.EffectiveStatus() != GroupAwaitingPayment {
			return ErrTransition
		}
		if !now.Before(g.PaymentDeadline()) {
			return ErrPaymentExpired
		}
	}
	for tenant := range orders {
		g := c.group(tenant)
		if !g.PaymentPending {
			g.NextPollAt, g.PollFailures = now.Add(PaymentOutcomeDelay), 0
		}
		g.PaymentPending = true
	}
	c.state.PaymentAttempt, c.state.PaymentStarted = token, true
	return c.touch(now)
}

// FinishPayment снимает неопределённость лишь у подтверждённых результатов этой попытки.
// AwaitingPayment после таймаута не доказывает, что отправленный платёж не завершится позже.
func (c *Checkout) FinishPayment(token string, result PaymentResult, now time.Time) error {
	if c.state.PaymentAttempt != token {
		return nil
	}
	for i := range c.state.Groups {
		g := &c.state.Groups[i]
		if result.Paid[g.TenantID] == g.OrderID && g.OrderID != "" && !g.Paid() {
			g.Status, g.LastOrderStatus = GroupPaid, GroupPaid
			g.Reason, g.LastErrorCode, g.PollToken = "", "", ""
			g.NextPollAt, g.PollFailures = time.Time{}, 0
			g.OrderStatusAt = now
		}
		if g.Paid() || result.Unpaid[g.TenantID] {
			g.PaymentPending = false
			if !g.Paid() {
				g.NextPollAt = g.PaymentDeadline().Add(PaymentExpiryGrace)
			}
		}
	}
	c.state.PaymentAttempt = ""
	c.state.PaymentStarted = c.paymentPending()
	c.rollup()
	return c.touch(now)
}
