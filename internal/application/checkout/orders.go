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

const orderBatch = 100

// CreateOrders фиксирует намерение и контакт до вызова магазина. Повтор использует тот же снимок.
func (s *Service) CreateOrders(
	ctx context.Context,
	buyerID,
	checkoutID ids.UUID,
	contact checkout.Contact,
) (checkout.State, error) {
	return s.prepareOrders(ctx, buyerID, checkoutID, contact)
}

// Resume работает и при запуске, и по расписанию. Отдельная покупка не задерживает остальные.
func (s *Service) Resume(ctx context.Context) error {
	var failures []error
	filter := checkout.Filter{Limit: orderBatch, OrdersOnly: true, WorkDueAt: s.clock.Now()}
	for {
		states, err := s.list(ctx, filter)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		for _, state := range states {
			if err := s.resumePurchase(ctx, state); err != nil {
				failures = append(failures, err)
			}
		}
		if len(states) < orderBatch {
			return errors.Join(failures...)
		}
		filter.AfterID = states[len(states)-1].ID
	}
}

// Purchases читает страницу снимков кабинета только для владельца.
func (s *Service) Purchases(ctx context.Context, buyerID ids.UUID, after ids.UUID) ([]order.Purchase, error) {
	states, err := s.list(ctx, checkout.Filter{
		BuyerID:    buyerID,
		AfterID:    after,
		Limit:      orderBatch,
		OrdersOnly: true,
		HasOrders:  true,
	})
	result := make([]order.Purchase, 0, len(states))
	for _, state := range states {
		result = append(result, order.Snapshot(state))
	}
	return result, err
}

// OperatorList показывает незавершённую отмену сразу, ожидание оплаты — после 15 минут.
func (s *Service) OperatorList(ctx context.Context, after ids.UUID, stuck bool) ([]checkout.State, error) {
	f := checkout.Filter{AfterID: after, Limit: orderBatch, OrdersOnly: true}
	if stuck {
		f.StuckBefore = s.clock.Now().Add(-15 * time.Minute)
	}
	return s.list(ctx, f)
}

func (s *Service) list(ctx context.Context, f checkout.Filter) ([]checkout.State, error) {
	var states []checkout.State
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		found, err := tx.Checkouts().List(ctx, f)
		if err != nil {
			return err
		}
		for _, c := range found {
			states = append(states, c.State())
		}
		return nil
	})
	return states, err
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
	c, err := s.load(ctx, buyerID, id)
	if err != nil {
		return err
	}
	var failures []error
	for _, g := range c.Groups() {
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
