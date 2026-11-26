package checkout

import (
	"context"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/order"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	orderBatch             = 100
	pickupHistoryLimit     = 5
	maxReadFailureLogLines = 10
)

// ListPage продолжает обход после последней прочитанной строки, даже если она не разбирается.
type ListPage[T any] struct {
	ReadDiagnostics
	Items []T
	Next  checkout.ListCursor
}

// ReadDiagnostics ограничивает общий журнал чтения покупок и продавцов и преобразования ответа.
type ReadDiagnostics struct {
	Issues   []ReadIssue
	observer ports.CheckoutReadObserver
	logged   int
}

// ReadIssue — безопасная диагностика покупки для панели.
type ReadIssue struct {
	CheckoutID ids.UUID     `json:"checkoutId"`
	TenantID   ids.TenantID `json:"tenantId"`
	Reason     string       `json:"reason"`
}

// CreateOrders фиксирует намерение и контакт до вызова магазина. Повтор использует тот же снимок.
func (s *Service) CreateOrders(
	ctx context.Context,
	buyerID,
	checkoutID ids.UUID,
	contact checkout.Contact,
) (checkout.State, error) {
	return s.prepareOrders(ctx, buyerID, checkoutID, contact)
}

// Purchases читает страницу снимков кабинета только для владельца.
func (s *Service) Purchases(
	ctx context.Context,
	buyerID ids.UUID,
	after ids.UUID,
) (ListPage[order.Purchase], error) {
	if buyerID == "" {
		return ListPage[order.Purchase]{}, checkout.ErrNotFound
	}
	states, err := s.list(ctx, checkout.Filter{
		BuyerID:    buyerID,
		AfterID:    after,
		Limit:      orderBatch,
		OrdersOnly: true,
		HasOrders:  true,
	})
	states.Issues = nil
	result := ListPage[order.Purchase]{
		ReadDiagnostics: states.ReadDiagnostics,
		Next:            states.Next,
		Items:           make([]order.Purchase, 0, len(states.Items)),
	}
	for _, state := range states.Items {
		result.Items = append(result.Items, order.Snapshot(state))
	}
	return result, err
}

// PickupHistory — пункты выдачи прошлых заказов покупателя для подсказки при выборе без чтения групп целиком.
func (s *Service) PickupHistory(ctx context.Context, buyerID ids.UUID) ([]checkout.Selection, error) {
	var page checkout.PickupHistoryPage
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		page, err = tx.Checkouts().PickupHistory(ctx, buyerID, pickupHistoryLimit)
		return err
	})
	if err != nil {
		return nil, err
	}
	diagnostics := s.ReadDiagnostics()
	for _, failure := range page.Failures {
		diagnostics.recordFailure(ctx, failure)
	}
	return page.Points, nil
}

// OperatorList показывает незавершённую отмену сразу, ожидание оплаты — после 15 минут.
func (s *Service) OperatorList(
	ctx context.Context,
	after checkout.ListCursor,
	stuck bool,
) (ListPage[checkout.State], error) {
	f := checkout.Filter{AfterID: after.ID, AfterStuckAt: after.StuckAt, Limit: orderBatch, OrdersOnly: true}
	if stuck {
		f.StuckBefore = s.clock.Now().Add(-15 * time.Minute)
	}
	return s.list(ctx, f)
}

func (s *Service) list(ctx context.Context, f checkout.Filter) (ListPage[checkout.State], error) {
	var found []checkout.ListEntry
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		found, err = tx.Checkouts().List(ctx, f)
		return err
	})
	if err != nil {
		return ListPage[checkout.State]{}, err
	}

	page := ListPage[checkout.State]{ReadDiagnostics: s.ReadDiagnostics()}
	for _, row := range found {
		page.Next = row.Cursor
		if len(row.Failures) > 0 {
			page.recordFailure(ctx, *row.ReadError())
		}
		for _, failure := range row.Failures {
			page.Issues = append(page.Issues, readIssue(failure.ID, failure.TenantID, failure.Reason))
		}
		if row.Checkout != nil {
			page.Items = append(page.Items, row.Checkout.State())
		}
	}
	return page, nil
}

// ReadDiagnostics начинает диагностику одного запроса или фонового обхода.
func (s *Service) ReadDiagnostics() ReadDiagnostics {
	return ReadDiagnostics{observer: s.observer}
}

// TakeLogSlot резервирует одну строку журнала для причины ошибки.
func (d *ReadDiagnostics) TakeLogSlot() bool {
	if d.logged >= maxReadFailureLogLines {
		return false
	}
	d.logged++
	return true
}

func (d *ReadDiagnostics) recordFailure(ctx context.Context, failure checkout.ReadFailure) {
	if d.observer == nil {
		return
	}
	d.observer.RecordCheckoutReadFailure(ctx, failure.TenantID, failure.ID, failure.Cause, d.TakeLogSlot())
}

// ReportReadRenderingFailure отмечает покупку, которую не удалось представить после чтения.
func (d *ReadDiagnostics) ReportReadRenderingFailure(
	ctx context.Context,
	state checkout.State,
	cause error,
) []ReadIssue {
	d.recordFailure(ctx, renderFailure(state, cause))
	if len(state.Groups) == 0 {
		return []ReadIssue{readIssue(state.ID, "", checkout.ReadRender)}
	}

	issues := make([]ReadIssue, 0, len(state.Groups))
	for _, group := range state.Groups {
		issues = append(issues, readIssue(state.ID, group.TenantID, checkout.ReadRender))
	}
	return issues
}

// UnreadableResponse сообщает об ошибке представления сохранённой покупки владельца.
func (d *ReadDiagnostics) UnreadableResponse(ctx context.Context, state checkout.State, cause error) error {
	d.recordFailure(ctx, renderFailure(state, cause))
	return checkout.ErrUnreadable
}

func renderFailure(state checkout.State, cause error) checkout.ReadFailure {
	failure := checkout.ReadFailure{ID: state.ID, BuyerID: state.BuyerID, Cause: cause, Reason: checkout.ReadRender}
	if len(state.Groups) > 0 {
		failure.TenantID = state.Groups[0].TenantID
	}
	return failure
}

func readIssue(id ids.UUID, tenant ids.TenantID, reason string) ReadIssue {
	return ReadIssue{CheckoutID: id, TenantID: tenant, Reason: reason}
}

// buyerReadError оставляет диагностику в журнале и раскрывает ошибку только владельцу.
func buyerReadError(ctx context.Context, buyerID ids.UUID, err error, observer ports.CheckoutReadObserver) error {
	var failure *checkout.ReadFailure
	if !errors.As(err, &failure) {
		return err
	}
	if failure.BuyerID != buyerID {
		return checkout.ErrNotFound
	}
	if observer != nil {
		observer.RecordCheckoutReadFailure(ctx, failure.TenantID, failure.ID, failure.Cause, true)
	}
	return checkout.ErrUnreadable
}

// consumeCart удаляет только количество из снимка, сохраняя добавленное после оформления.
func (s *Service) consumeCart(ctx context.Context, tx ports.Tx, c *checkout.Checkout) error {
	if c.State().CartConsumed {
		return nil
	}
	basket, err := tx.Carts().ByBuyer(ctx, c.BuyerID())
	if err != nil && !errors.Is(err, cart.ErrNotFound) {
		return err
	}
	now, ttl := s.clock.Now(), s.cartTTL()
	if basket != nil && !basket.Expired(now) {
		for _, g := range c.Groups() {
			if g.Excluded() {
				continue
			}
			for _, line := range g.Lines {
				basket.Consume(line.Offer, line.Quantity, now, ttl)
			}
		}
		if err := tx.Carts().Save(ctx, basket); err != nil {
			return err
		}
	}
	if _, err := c.MarkCartConsumed(now); err != nil {
		return err
	}
	return nil
}

// ReturnCancelled изолирует возврат каждой группы от остальных групп и сверки заказов.
func (s *Service) ReturnCancelled(ctx context.Context, buyerID, id ids.UUID) error {
	c, err := s.LoadForBuyer(ctx, buyerID, id)
	if err != nil {
		return err
	}
	return s.returnDue(ctx, c.State())
}

// returnDue возвращает в корзину созревшие группы покупки, каждую своей транзакцией.
func (s *Service) returnDue(ctx context.Context, state checkout.State) error {
	buyerID, id := state.BuyerID, state.ID
	var failures []error
	for _, g := range state.Groups {
		if !g.ReturnDue(s.clock.Now()) {
			continue
		}
		if err := s.returnCancelledGroup(ctx, buyerID, id, g.TenantID); err != nil {
			// Транзакция возврата откатилась. Запоминаем текущий остаток отдельно:
			// временная проблема корзины не должна закрывать оплату другого магазина.
			if deferred := s.deferCartReturn(ctx, buyerID, id, g.TenantID, err); deferred != nil {
				failures = append(failures, errors.Join(err, deferred))
			}
		}
	}
	return errors.Join(failures...)
}

func (s *Service) returnCancelledGroup(ctx context.Context, buyerID, id ids.UUID, tenant ids.TenantID) error {
	after := func(ctx context.Context, tx ports.Tx, c *checkout.Checkout) error {
		return s.returnGroupInTx(ctx, tx, c, tenant)
	}
	_, err := s.updateSelection(ctx, buyerID, id, after, func(*checkout.Checkout, time.Time) error {
		return nil
	})
	return err
}

func (s *Service) returnGroupInTx(
	ctx context.Context,
	tx ports.Tx,
	c *checkout.Checkout,
	tenant ids.TenantID,
) error {
	now, ttl := s.clock.Now(), s.cartTTL()
	for _, g := range c.Groups() {
		if g.TenantID != tenant || !g.ReturnDue(now) {
			continue
		}
		basket, err := tx.Carts().ByBuyer(ctx, c.BuyerID())
		if errors.Is(err, cart.ErrNotFound) {
			basket, err = cart.New(s.ids.NewUUID(), c.BuyerID(), now, ttl), nil
		}
		if err != nil {
			return err
		}
		basket.ResetIfExpired(now, ttl)
		lines := g.Lines
		if g.ReturnFailed {
			lines = g.ReturnRemaining
		}
		remaining, reason := returnGroup(basket, lines, now, ttl)
		// Новый код покупателя важнее снимка покупки; отказ того же кода сохраняем с причиной.
		if g.PromoCode != "" && basket.State().Promos[tenant].Code == "" {
			_, err := basket.SetPromo(tenant, g.PromoCode, now, ttl)
			if err != nil && !errors.Is(err, cart.ErrLineNotFound) {
				return err
			}
		}
		if g.PromoCode != "" && basket.State().Promos[tenant].Code == g.PromoCode {
			if g.LastErrorCode == checkout.ErrPromoInvalid.Key() {
				message := g.Reason
				if message == "" {
					message = checkout.ErrPromoInvalid.Message()
				}
				basket.ApplyPromo(tenant, cart.PromoCode{Code: g.PromoCode, Message: &message}, nil, now, ttl)
			}
		}
		if _, err := c.RecordCartReturn(tenant, remaining, reason, now); err != nil {
			return err
		}
		return tx.Carts().Save(ctx, basket)
	}
	return nil
}

func (s *Service) deferCartReturn(ctx context.Context, buyerID, id ids.UUID, tenant ids.TenantID, cause error) error {
	reason := "cart.return_failed"
	if typed, ok := errs.As(cause); ok {
		reason = typed.Key()
	}
	_, err := s.update(ctx, buyerID, id, func(c *checkout.Checkout, now time.Time) error {
		for _, g := range c.Groups() {
			if g.TenantID != tenant || !g.ReturnDue(now) {
				continue
			}
			remaining := g.Lines
			if g.ReturnFailed {
				remaining = g.ReturnRemaining
			}
			_, err := c.RecordCartReturn(tenant, remaining, reason, now)
			return err
		}
		return nil
	})
	return err
}

func returnGroup(basket *cart.Cart, lines []checkout.Line, now time.Time, ttl time.Duration) ([]checkout.Line, string) {
	var remaining []checkout.Line
	reason := ""
	for _, line := range lines {
		restored, err := basket.ReturnUpTo(
			cart.Line{
				Offer:    line.Offer,
				Name:     line.Name,
				Quantity: line.Quantity,
				Price:    line.Price,
			},
			now,
			ttl,
		)
		if err != nil {
			reason = "cart.return_failed"
			if typed, ok := errs.As(err); ok {
				reason = typed.Key()
			}
		}
		if restored < line.Quantity {
			line.Quantity -= restored
			remaining = append(remaining, line)
		}
	}
	return remaining, reason
}

func (s *Service) cartTTL() time.Duration {
	if s.config.CartTTL > 0 {
		return s.config.CartTTL
	}
	return 30 * 24 * time.Hour
}
