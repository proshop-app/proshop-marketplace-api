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
	// PaymentExpiryGrace даёт событию отмены дойти после точной задачи магазина.
	PaymentExpiryGrace = 2 * time.Minute
	// PaymentOutcomeDelay даёт запросу оплаты завершиться до страховочного GET.
	PaymentOutcomeDelay = 30 * time.Second
	// Отказ отмены означает исполнение заказа: события ведут его, редкий GET страхует их потерю.
	blockedCancellationPoll = 10 * time.Minute
	// ReconcileInFlightFor — сколько заказ сверяется, не меняя статуса: дольше исполняемый заказ —
	// забота оператора, а не ежесуточного запроса магазину.
	ReconcileInFlightFor = 60 * 24 * time.Hour
)

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

// PollDue страхует потерю события после срока и неизвестный исход отправленной оплаты.
func (g Group) PollDue(now time.Time) bool {
	at, found := g.pollAt()
	return found && !now.Before(at)
}

func (g Group) pollAt() (time.Time, bool) {
	if g.Status == GroupCreationExpired || g.OrderID == "" {
		return time.Time{}, false
	}
	if g.Abandoned {
		return g.NextPollAt, g.CancellationBlocked && g.ReconciliationRequired
	}
	if g.Status == GroupCancelled || paidStatus(g.EffectiveStatus()) {
		return time.Time{}, false
	}
	at := g.NextPollAt
	if !g.PaymentPending && at.Before(g.PaymentDeadline().Add(PaymentExpiryGrace)) {
		at = g.PaymentDeadline().Add(PaymentExpiryGrace)
	}
	return at, true
}

// ReconcileUntil — до какого времени заказ участвует в суточной сверке пачкой; ноль — не участвует.
// Срок считается от последней смены статуса: набор сверки растёт с числом живых заказов,
// а не с возрастом площадки. Полученный заказ не сверяется: возврат приходит событием магазина
// (решение владельца 2026-11-22).
func (g Group) ReconcileUntil() time.Time {
	if g.OrderID == "" || g.Abandoned {
		return time.Time{}
	}
	switch g.EffectiveStatus() {
	case GroupPaid, GroupAssembled, GroupShipped, GroupAwaitingPickup:
		since := g.OrderStatusAt
		if since.IsZero() {
			// Статус сохранён без времени (до сверки или отказом отмены): срок — от создания заказа.
			since = g.AwaitingSince
		}
		if since.IsZero() {
			return time.Time{}
		}
		return since.Add(ReconcileInFlightFor)
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
	now time.Time,
) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if g.PollToken != token {
		return false, nil
	}
	changed, err := c.Observe(tenant, order, now)
	if err == nil {
		g.schedulePollResult(now)
		g.PollToken = ""
		g.Tracking = cloneTracking(order.Tracking)
		if !changed {
			return true, c.touch(now)
		}
	}
	return changed, err
}

func (g *Group) schedulePollResult(now time.Time) {
	if _, needed := g.pollAt(); !needed {
		g.NextPollAt = time.Time{}
		return
	}
	if g.Abandoned {
		g.NextPollAt = now.Add(blockedCancellationPoll)
		return
	}
	// Долгая просрочка требует более редкой сверки, но успешное ожидание оплаты не является отказом.
	delay := min(max(now.Sub(g.PaymentDeadline().Add(PaymentExpiryGrace)), OrderRetryBase), OrderSlowPoll)
	g.NextPollAt = now.Add(delay)
}

func (g Group) pollRetryDelay() time.Duration {
	if g.PollFailures >= MaxOrderFailures && !g.Abandoned {
		return OrderSlowPoll
	}
	return retryDelay(g.PollFailures)
}

// ApplyOrderState снимает страховочную проверку, когда событие уже принесло итог магазина.
// Заказ, отличный от заказа группы, — ErrNotFound: событие относится не к этой покупке. Группе, ещё
// не знающей номера заказа, событие обогнало ответ создания: состояние сохраняется до привязки.
// Отмена неоплаченного заказа значима только для группы, ждущей оплату: отмену по команде площадки
// ведёт её собственный сценарий, и событие магазина не должно подменить его причину и итог.
// Отслеживание без номера перехода не отзывается: состояние без него прежнее не стирает.
func (c *Checkout) ApplyOrderState(tenant ids.TenantID, order SellerOrder, now time.Time) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if g.OrderID == "" {
		return c.holdEarlyOrder(g, order, now)
	}
	if g.OrderID != order.OrderID {
		return false, ErrNotFound
	}
	ownCancellation := order.Status == GroupCancelled &&
		(g.Abandoned || g.EffectiveStatus() != GroupAwaitingPayment)
	if ownCancellation || g.outranUnnumbered(order) {
		return false, nil
	}
	changed, err := c.Observe(tenant, order, now)
	if err != nil || !changed {
		return false, err
	}
	g.PollToken = ""
	if order.Tracking != nil {
		g.Tracking = cloneTracking(order.Tracking)
	}
	return true, nil
}

// holdEarlyOrder сохраняет состояние заказа, о котором магазин сообщил раньше ответа создания (ответ
// потерян и сохранится повтором или поздно): старшая ревизия перекрывает младшую, повтор — не изменение.
func (c *Checkout) holdEarlyOrder(g *Group, order SellerOrder, now time.Time) (bool, error) {
	if g.EarlyOrder != nil && g.EarlyOrder.Revision >= order.Revision {
		return false, nil
	}
	early := order
	early.Tracking = cloneTracking(order.Tracking)
	g.EarlyOrder = &early
	return true, c.touch(now)
}

// applyEarlyOrder применяет при привязке заказа состояние из раннего события. Состояние другого
// заказа или противоречащее порядку исполнения отбрасывается, как такое же событие после привязки.
func (c *Checkout) applyEarlyOrder(g *Group, now time.Time) {
	early := g.EarlyOrder
	g.EarlyOrder = nil
	if early != nil && early.OrderID == g.OrderID {
		_, _ = c.ApplyOrderState(g.TenantID, *early, now)
	}
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
	g.NextPollAt = now.Add(g.pollRetryDelay())
	if g.PollFailures >= MaxOrderFailures && !g.Abandoned {
		if g.Status != GroupSellerUnreachable {
			g.LastOrderStatus = g.Status
		}
		g.Status, g.Reason = GroupSellerUnreachable, reason
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
	next, found := g.pollAt()
	if g.ReturnDue(g.NextReturnAt) && (!found || g.NextReturnAt.Before(next)) {
		next, found = g.NextReturnAt, true
	}
	return next, found
}
