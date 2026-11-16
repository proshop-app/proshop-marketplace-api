package checkout

import "time"

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
	if c.Status() != StatusCreatingOrders || !deadline.After(now) {
		return ErrTransition
	}
	for i := range c.state.Groups {
		c.state.Groups[i].CreationDeadline = deadline
	}
	return c.touch(now)
}

// CreationExpired отмечает достижение срока; закрытие отдельно учитывает активную аренду попытки.
func (g Group) CreationExpired(now time.Time) bool {
	return !g.CreationDeadline.IsZero() && !now.Before(g.CreationDeadline) &&
		!g.Abandoned && !g.CreateExhausted && g.OrderID == ""
}

// ExpireCreation отделяет итог для покупателя от незавершённой сверки магазина.
func (c *Checkout) ExpireCreation(now time.Time) (bool, error) {
	changed := false
	for i := range c.state.Groups {
		g := &c.state.Groups[i]
		if !g.CreationExpired(now) || (g.CreateToken != "" && now.Before(g.NextCreateAt)) {
			continue
		}
		g.ReconciliationRequired = g.CreateToken != "" || g.CreateFailures > 0
		g.Abandoned, g.CreateExhausted = true, true
		g.Status, g.Reason = GroupUnconfirmed, "creation_deadline_exceeded"
		changed = true
	}
	if !changed {
		return false, nil
	}
	c.rollup()
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
	if g.Returnable() {
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
	if c.state.PaymentStarted || c.Status() == StatusPaid || c.Status() == StatusPartiallyPaid {
		return false, ErrTransition
	}
	if c.Status() == StatusDraft || c.Status() == StatusConfirmed {
		return false, ErrTransition
	}
	changed := false
	for i := range c.state.Groups {
		g := &c.state.Groups[i]
		if g.Returnable() {
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
	if !changed {
		return false, nil
	}
	c.rollup()
	return true, c.touch(now)
}

// BeginPayment исключает гонку отмены с уже отправленным внешним платежом.
func (c *Checkout) BeginPayment(now time.Time) error {
	if _, err := c.PaymentAmount(); err != nil {
		return err
	}
	if err := c.CheckPaymentWindow(now); err != nil {
		return err
	}
	if c.state.PaymentStarted {
		return nil
	}
	c.state.PaymentStarted = true
	return c.touch(now)
}

// ClaimPayment ограждает снятие блокировки: отказ старой попытки не отменяет защиту новой.
func (c *Checkout) ClaimPayment(token string, now time.Time) error {
	if err := c.BeginPayment(now); err != nil {
		return err
	}
	c.state.PaymentAttempt = token
	return c.touch(now)
}

// FinishPayment снимает блокировку отмены только при доказанном отсутствии оплаты.
func (c *Checkout) FinishPayment(token string, unpaid bool, now time.Time) error {
	if c.state.PaymentAttempt != token {
		return nil
	}
	c.state.PaymentAttempt = ""
	if unpaid {
		for _, g := range c.state.Groups {
			switch g.EffectiveStatus() {
			case GroupPaid, GroupShipped, GroupDelivered, GroupReturned:
				return ErrTransition
			}
		}
		c.state.PaymentStarted = false
	}
	return c.touch(now)
}
