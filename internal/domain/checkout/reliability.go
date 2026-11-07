package checkout

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// MaxOrderFailures — после пяти отказов создание останавливается, опрос становится редким.
	MaxOrderFailures = 5
	// OrderCallLease — захват попытки переживает обрыв процесса и ограничивает конкурентные вызовы.
	OrderCallLease = time.Minute
	// OrderRetryBase — первая задержка после отказа.
	OrderRetryBase = 30 * time.Second
	// OrderRetryCap — максимальная задержка обычных повторов.
	OrderRetryCap = 10 * time.Minute
	// OrderSlowPoll — сверка магазина, который долго не отвечает.
	OrderSlowPoll = 30 * time.Minute
)

// RecheckDelivery заменяет расчёты до первого заказа. Изменение платы возвращает покупку к подтверждению.
func (c *Checkout) RecheckDelivery(quotes map[ids.TenantID]delivery.Quote, now time.Time) (bool, error) {
	if c.Status() != StatusConfirmed {
		return false, ErrTransition
	}
	if len(quotes) != len(c.state.Groups) {
		return false, ErrInvalidQuotes
	}
	changed := false
	for _, g := range c.state.Groups {
		q, ok := quotes[g.TenantID]
		if !ok {
			return false, ErrInvalidQuotes
		}
		if q.Status != delivery.QuoteAvailable || q.Expired(now) || q.BuyerCost != g.DeliveryPrice {
			changed = true
		}
	}
	for i := range c.state.Groups {
		q := quotes[c.state.Groups[i].TenantID]
		c.state.Groups[i].Quote = &q
	}
	if changed {
		c.state.Status = StatusDraft
		c.state.ConfirmedAt = time.Time{}
	}
	return changed, c.touch(now)
}

// CreationDue отличает повтор создания от сверки уже созданного заказа.
func (g Group) CreationDue(now time.Time) bool {
	return g.OrderID == "" && (g.Status == GroupCreating || g.Status == GroupSellerUnreachable) &&
		!g.CreateExhausted && !now.Before(g.NextCreateAt)
}

// PollDue учитывает срок следующего обращения и окончательные статусы.
func (g Group) PollDue(now time.Time) bool {
	return g.OrderID != "" && g.Status != GroupCancelled && g.Status != GroupReturned && !now.Before(g.NextPollAt)
}

// Returnable — группа без исполненного заказа, которую нужно вернуть в корзину.
func (g Group) Returnable() bool {
	status := g.EffectiveStatus()
	return status == GroupCancelled || status == GroupPriceChanged || status == GroupDeliveryUnavailable
}

// ReturnDue ограничивает повтор частичного возврата в переполненную корзину.
func (g Group) ReturnDue(now time.Time) bool {
	return g.Returnable() && !g.CartReturned && !now.Before(g.NextReturnAt)
}

// ClaimCreation фиксирует захват до внешнего вызова. Параллельный обработчик пропускает группу.
func (c *Checkout) ClaimCreation(tenant ids.TenantID, token string, now time.Time) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if !g.CreationDue(now) {
		return false, nil
	}
	g.CreateToken, g.NextCreateAt = token, now.Add(OrderCallLease)
	return true, c.touch(now)
}

// CompleteCreation применяет только ответ текущей попытки, сохраняя неизменный запрос при повторах.
func (c *Checkout) CompleteCreation(
	tenant ids.TenantID,
	token string,
	order SellerOrder,
	now time.Time,
) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if g.CreateToken != token {
		return false, nil
	}
	changed, err := c.AttachOrder(tenant, order, now)
	if err == nil {
		g.CreateToken = ""
		g.CreateFailures = 0
		g.LastErrorCode = ""
	}
	return changed, err
}

// FailCreation отделяет окончательные отказы от ограниченных повторов недоступного магазина.
func (c *Checkout) FailCreation(
	tenant ids.TenantID,
	token, code, reason string,
	terminal GroupStatus,
	prices []PriceChange,
	now time.Time,
) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if g.CreateToken != token || g.OrderID != "" || g.Returnable() {
		return false, nil
	}
	g.CreateToken = ""
	g.CreateFailures++
	g.Status, g.Reason = GroupSellerUnreachable, reason
	g.LastErrorCode = code
	g.CurrentPrices = append([]PriceChange(nil), prices...)
	g.NextCreateAt = now.Add(retryDelay(g.CreateFailures))
	if terminal != "" {
		g.Status = terminal
	}
	if g.CreateFailures >= MaxOrderFailures || terminal != "" {
		g.CreateExhausted = true
	}
	c.rollup()
	return true, c.touch(now)
}

// ClaimPoll записывает NextPollAt до запроса: GET не обходит ограничение частоты.
func (c *Checkout) ClaimPoll(tenant ids.TenantID, token string, now time.Time) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if !g.PollDue(now) {
		return false, nil
	}
	g.PollToken, g.NextPollAt = token, now.Add(OrderCallLease)
	return true, c.touch(now)
}

// CompletePoll не даёт позднему ответу отменить результат новой попытки.
func (c *Checkout) CompletePoll(
	tenant ids.TenantID,
	token string,
	order SellerOrder,
	next, now time.Time,
) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if g.PollToken != token {
		return false, nil
	}
	changed, err := c.Observe(tenant, order, next, now)
	if err == nil {
		g.PollToken = ""
		g.Tracking = cloneTracking(order.Tracking)
		if !changed {
			return true, c.touch(now)
		}
	}
	return changed, err
}

// FailPoll сохраняет задержку даже при неизвестном статусе; после серии ошибок опрос становится редким.
func (c *Checkout) FailPoll(tenant ids.TenantID, token, reason string, now time.Time) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if g.PollToken != token {
		return false, nil
	}
	g.PollToken = ""
	g.LastErrorCode, g.Reason = reason, reason
	if g.PollFailures < MaxOrderFailures {
		g.PollFailures++
	}
	g.NextPollAt = now.Add(retryDelay(g.PollFailures))
	if g.PollFailures >= MaxOrderFailures {
		if g.Status != GroupSellerUnreachable {
			g.LastOrderStatus = g.Status
		}
		g.Status, g.Reason = GroupSellerUnreachable, reason
		g.NextPollAt = now.Add(OrderSlowPoll)
	}
	c.rollup()
	return true, c.touch(now)
}

// RecordCartReturn сохраняет только остаток: повтор не дублирует уже возвращённые количества.
func (c *Checkout) RecordCartReturn(
	tenant ids.TenantID,
	remaining []Line,
	reason string,
	now time.Time,
) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if !g.Returnable() {
		return false, ErrTransition
	}
	if g.CartReturned {
		return false, nil
	}
	g.ReturnRemaining = append([]Line(nil), remaining...)
	g.CartReturned, g.ReturnFailed = len(remaining) == 0, len(remaining) > 0
	g.ReturnReason = reason
	g.NextReturnAt = now.Add(time.Minute)
	return true, c.touch(now)
}

func retryDelay(failures int) time.Duration {
	delay := OrderRetryBase
	for i := 1; i < failures && delay < OrderRetryCap; i++ {
		delay *= 2
	}
	if delay > OrderRetryCap {
		return OrderRetryCap
	}
	return delay
}
