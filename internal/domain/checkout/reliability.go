package checkout

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Паузы и ограничения повторов создания и сверки.
const (
	// MaxOrderFailures — после пяти отказов опрос становится редким.
	MaxOrderFailures = 5
	// OrderPollLease ограничивает конкурентные опросы уже созданного заказа.
	OrderPollLease = time.Minute
	// OrderRetryBase — первая задержка после отказа.
	OrderRetryBase     = 30 * time.Second
	CreationRetryDelay = time.Second
	// OrderRetryCap — максимальная задержка обычных повторов.
	OrderRetryCap = 10 * time.Minute
	// OrderSlowPoll — сверка магазина, который долго не отвечает.
	OrderSlowPoll = 30 * time.Minute
	// ReconcileDeliveredFor — сколько полученный заказ остаётся в страховочной сверке:
	// возврат позже этого срока площадка узнаёт только из события магазина.
	ReconcileDeliveredFor = 30 * 24 * time.Hour
	// ReconcileInFlightFor — сколько заказ сверяется, не меняя статуса: дольше исполняемый заказ —
	// забота оператора, а не ежесуточного запроса магазину.
	ReconcileInFlightFor = 60 * 24 * time.Hour
)

// reconcileOpen — срок сверки заказа без времени статуса (сохранён до появления сверки):
// первое же изменение статуса ограничит его.
var reconcileOpen = time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC)

// RecheckDelivery заменяет расчёты до первого заказа. Изменение платы возвращает покупку к подтверждению.
func (c *Checkout) RecheckDelivery(quotes map[ids.TenantID]delivery.Quote, now time.Time) (bool, error) {
	if c.Status() != StatusConfirmed {
		return false, ErrTransition
	}
	changed := false
	available := 0
	for _, g := range c.state.Groups {
		if g.Excluded() {
			continue
		}
		if g.OrderID != "" {
			available++
			continue
		}
		q, ok := quotes[g.TenantID]
		if !ok {
			return false, ErrInvalidQuotes
		}
		if g.QuotePending() || q.Reason == delivery.ReasonSellerUnreachable {
			return false, ErrSellerUnavailable
		}
		if q.Status == delivery.QuoteAvailable {
			available++
		}
		if g.DeliveryChanged(q) {
			changed = true
		}
	}
	changed = changed || available == 0
	for i := range c.state.Groups {
		g := &c.state.Groups[i]
		if g.Excluded() || g.OrderID != "" {
			continue
		}
		q := quotes[g.TenantID]
		g.Quote = &q
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
		!g.Abandoned && !g.CreateExhausted && !g.CreationExpired(now) && !now.Before(g.NextCreateAt)
}

// PollDue оставляет опрос только неоплаченному заказу: его окно оплаты коротко, а подтверждение
// денег не должно зависеть от одного канала. Статусы оплаченного заказа приходят событием магазина.
// Отказ отмены уже исполняемого заказа сверяется опросом до его завершения, как прежде.
func (g Group) PollDue(now time.Time) bool {
	if g.Status == GroupCreationExpired {
		return false
	}
	if g.Abandoned {
		return g.CancellationBlocked && g.ReconciliationRequired && g.OrderID != "" && !now.Before(g.NextPollAt)
	}
	return g.OrderID != "" && g.Status != GroupCancelled && !paidStatus(g.EffectiveStatus()) &&
		!now.Before(g.NextPollAt)
}

// ReconcileUntil — до какого времени заказ участвует в суточной сверке пачкой; ноль — не участвует.
// Срок считается от последней смены статуса: набор сверки растёт с числом живых заказов,
// а не с возрастом площадки. Полученный заказ сверяется ради возврата.
func (g Group) ReconcileUntil() time.Time {
	if g.OrderID == "" || g.Abandoned {
		return time.Time{}
	}
	switch g.EffectiveStatus() {
	case GroupPaid, GroupAssembled, GroupShipped, GroupAwaitingPickup:
		if g.OrderStatusAt.IsZero() {
			return reconcileOpen
		}
		return g.OrderStatusAt.Add(ReconcileInFlightFor)
	case GroupDelivered:
		if g.OrderStatusAt.IsZero() {
			return time.Time{}
		}
		return g.OrderStatusAt.Add(ReconcileDeliveredFor)
	}
	return time.Time{}
}

// SettleWork отмечает покупку изменённой, если её выбрали на фоновую работу, а созревшей работы
// у групп нет: сохранение пересчитает сроки, и покупка перестанет выбираться каждый круг.
func (c *Checkout) SettleWork(now time.Time) error {
	for _, g := range c.state.Groups {
		if next, found := g.NextWorkAt(); found && !now.Before(next) {
			return nil
		}
	}
	return c.touch(now)
}

// Returnable — группа без исполненного заказа, которую нужно вернуть в корзину.
func (g Group) Returnable() bool {
	if g.Paid() {
		return false
	}
	status := g.EffectiveStatus()
	return status == GroupCancellationRequested || status == GroupRejected ||
		status == GroupUnconfirmed || status == GroupCancelled ||
		status == GroupPriceChanged || status == GroupDeliveryUnavailable
}

// ReturnDue ограничивает повтор частичного возврата в переполненную корзину.
func (g Group) ReturnDue(now time.Time) bool {
	return g.Returnable() && !g.CartReturned && !now.Before(g.NextReturnAt)
}

// ClaimCreation фиксирует захват до внешнего вызова. Параллельный обработчик пропускает группу.
func (c *Checkout) ClaimCreation(tenant ids.TenantID, token string, leaseUntil, now time.Time) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if c.Status() == StatusDraft || c.Status() == StatusConfirmed || !g.CreationDue(now) {
		return false, nil
	}
	g.CreateToken, g.NextCreateAt = token, leaseUntil
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
		progress := g.CreateToken != "" || g.CreateFailures != 0 || g.LastErrorCode != ""
		g.CreateToken = ""
		g.CreateFailures = 0
		g.LastErrorCode = ""
		if !changed && progress {
			return true, c.touch(now)
		}
	}
	return changed, err
}

// FailCreation отделяет окончательные отказы от ограниченных повторов недоступного магазина.
func (c *Checkout) FailCreation(
	tenant ids.TenantID,
	token, code, reason string,
	terminal GroupStatus,
	reconciliation bool,
	prices []PriceChange,
	now time.Time,
) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if g.CreateToken != token || g.OrderID != "" {
		return false, nil
	}
	if g.Abandoned {
		g.CreateToken = ""
		g.LastErrorCode = code
		return true, c.touch(now)
	}
	if g.Returnable() {
		return false, nil
	}
	g.CreateToken = ""
	g.CreateFailures++
	g.Status, g.Reason = GroupSellerUnreachable, reason
	g.LastErrorCode = code
	g.CurrentPrices = append([]PriceChange(nil), prices...)
	g.NextCreateAt = now.Add(CreationRetryDelay)
	if terminal != "" {
		g.Status = terminal
		g.ReconciliationRequired = reconciliation
		g.CreateExhausted = true
		g.Quote = nil
	}
	c.rollup()
	if terminal != "" {
		c.state.Status, c.state.ConfirmedAt = StatusDraft, time.Time{}
	}
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
	g.PollToken, g.NextPollAt = token, now.Add(OrderPollLease)
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

// ApplyOrderState принимает состояние заказа из события магазина или сверки: срок опроса не меняется.
// Заказ, которого у группы нет, — ErrNotFound: событие относится не к этой покупке.
// Отмена неоплаченного заказа значима только для группы, ждущей оплату: отмену по команде площадки
// ведёт её собственный сценарий, и событие магазина не должно подменить его причину и итог.
// Отслеживание без номера перехода не отзывается: состояние без него прежнее не стирает.
func (c *Checkout) ApplyOrderState(tenant ids.TenantID, order SellerOrder, now time.Time) (bool, error) {
	g := c.group(tenant)
	if g == nil || g.OrderID == "" || g.OrderID != order.OrderID {
		return false, ErrNotFound
	}
	ownCancellation := order.Status == GroupCancelled &&
		(g.Abandoned || g.EffectiveStatus() != GroupAwaitingPayment)
	if ownCancellation || g.outranUnnumbered(order) {
		return false, nil
	}
	changed, err := c.Observe(tenant, order, g.NextPollAt, now)
	if err != nil || !changed {
		return false, err
	}
	g.PollToken = ""
	if order.Tracking != nil {
		g.Tracking = cloneTracking(order.Tracking)
	}
	return true, nil
}

// outranUnnumbered — статус группы получен без номера перехода (оплата площадкой, отказ отмены)
// и уже дальше пришедшего: такое состояние опоздало, а не противоречит порядку исполнения.
func (g Group) outranUnnumbered(order SellerOrder) bool {
	known := g.EffectiveStatus()
	if g.Abandoned {
		known = g.LastOrderStatus
	}
	return g.OrderRevision == 0 && orderStage(order.Status) < orderStage(known)
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
	g.LastErrorCode = reason
	if !g.CancellationBlocked {
		g.Reason = reason
	}
	if g.PollFailures < MaxOrderFailures {
		g.PollFailures++
	}
	g.NextPollAt = now.Add(retryDelay(g.PollFailures))
	if g.PollFailures >= MaxOrderFailures && !g.Abandoned {
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

// NextWorkAt возвращает ближайшую сверку или возврат; нулевое время означает работу сразу.
func (g Group) NextWorkAt() (time.Time, bool) {
	var next time.Time
	found := g.PollDue(g.NextPollAt)
	if found {
		next = g.NextPollAt
	}
	if g.ReturnDue(g.NextReturnAt) && (!found || g.NextReturnAt.Before(next)) {
		next, found = g.NextReturnAt, true
	}
	return next, found
}
