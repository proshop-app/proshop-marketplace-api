package checkout

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CancellationRequired выбирает команду вместо опроса в ожидании отмены.
func (g Group) CancellationRequired() bool {
	return !g.Paid() && g.ReconciliationRequired && !g.CancellationConfirmed && !g.CancellationBlocked
}

// CompleteCancellation принимает только подтверждённый сервером запрет создания по ключу.
func (c *Checkout) CompleteCancellation(tenant ids.TenantID, orderID string, now time.Time) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if g.CancellationConfirmed {
		return false, nil
	}
	if !g.ReconciliationRequired {
		return false, nil
	}
	if g.OrderID != "" && orderID != g.OrderID {
		return false, ErrInvalidOrder
	}
	if !g.Abandoned && g.Status != GroupCreationExpired {
		g.Status = g.EffectiveStatus()
		g.Abandoned = true
	}
	if orderID != "" {
		g.OrderID = orderID
	}
	g.CancellationLastErrorCode = ""
	g.CancellationConfirmed = true
	g.CancellationBlocked, g.ReconciliationRequired = false, false
	if g.Status == GroupCancellationRequested {
		g.Status = GroupCancelled
	}
	g.LastOrderStatus = GroupCancelled
	c.rollup()
	return true, c.touch(now)
}

// BlockCancellation не подменяет локальный итог оплаченного заказа успешной отменой.
func (c *Checkout) BlockCancellation(
	tenant ids.TenantID,
	orderID string,
	status GroupStatus,
	reason string,
	now time.Time,
) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if g.CancellationConfirmed || !g.ReconciliationRequired {
		return false, nil
	}
	if orderID == "" || (g.OrderID != "" && g.OrderID != orderID) {
		return false, ErrInvalidOrder
	}
	if g.CancellationBlocked && g.LastOrderStatus == status && g.Reason == reason {
		return false, nil
	}
	if !g.Abandoned && g.Status != GroupCreationExpired {
		g.Status = g.EffectiveStatus()
		g.Abandoned = true
	}
	if g.AwaitingSince.IsZero() {
		g.AwaitingSince = now
	}
	g.OrderID, g.LastOrderStatus = orderID, status
	if g.Status == GroupCreationExpired {
		observed := Group{Status: status}
		if observed.Paid() {
			g.Status, g.Reason = status, reason
			g.CancellationBlocked, g.ReconciliationRequired = false, false
			g.NextPollAt = now
			c.state.Status = StatusCreatingOrders
			c.rollup()
			return true, c.touch(now)
		}
	}
	g.CancellationBlocked = true
	g.Reason = reason
	g.NextPollAt = now
	return true, c.touch(now)
}

// MarkCancellationQueued защищает от повторной постановки при конкурентных переходах покупки.
func (c *Checkout) MarkCancellationQueued(tenant ids.TenantID, now time.Time) error {
	g := c.group(tenant)
	if g == nil {
		return ErrNotFound
	}
	if g.CancellationQueued {
		return nil
	}
	if !g.CancellationRequired() {
		return ErrTransition
	}
	g.CancellationQueued = true
	return c.touch(now)
}

// BeginCancellationAttempt сохраняет число HTTP-попыток независимо от результата вызова.
func (c *Checkout) BeginCancellationAttempt(tenant ids.TenantID, now time.Time) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if !g.CancellationRequired() {
		return false, nil
	}
	g.CancellationAttempts++
	return true, c.touch(now)
}

// FailCancellation оставляет неисправимый ответ оператору, не повторяя опасную команду.
func (c *Checkout) FailCancellation(tenant ids.TenantID, code string, blocked bool, now time.Time) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if !g.CancellationRequired() {
		return false, nil
	}
	g.CancellationLastErrorCode = code
	if blocked {
		if !g.Abandoned && g.Status != GroupCreationExpired {
			g.Status = g.EffectiveStatus()
			g.Abandoned = true
		}
		g.CancellationBlocked = true
		g.Reason = code
		g.NextPollAt = now
	}
	return true, c.touch(now)
}
