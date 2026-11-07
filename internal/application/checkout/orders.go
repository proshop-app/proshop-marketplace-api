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
	orderBatch = 100
	unpaidPoll = 30 * time.Second
	paidPoll   = 10 * time.Minute
)

// CreateOrders фиксирует намерение и контакт до вызова магазина. Повтор использует тот же снимок.
func (s *Service) CreateOrders(
	ctx context.Context,
	buyerID,
	checkoutID ids.UUID,
	contact checkout.Contact,
) (checkout.State, error) {
	if _, err := s.Get(ctx, buyerID, checkoutID); err != nil {
		return checkout.State{}, err
	}
	if !s.config.PaymentStub {
		return checkout.State{}, checkout.ErrPaymentUnavailable
	}
	state, err := s.prepareOrders(ctx, buyerID, checkoutID, contact)
	if err != nil {
		return state, err
	}

	if err := s.consumeCart(ctx, buyerID, checkoutID); err != nil {
		return checkout.State{}, err
	}
	return s.createGroups(ctx, buyerID, checkoutID)
}

func (s *Service) createGroups(ctx context.Context, buyerID, id ids.UUID) (checkout.State, error) {
	c, err := s.load(ctx, buyerID, id)
	if err != nil {
		return checkout.State{}, err
	}
	for _, g := range c.Groups() {
		if !g.CreationDue(s.clock.Now()) {
			continue
		}
		if err := s.createGroup(ctx, buyerID, id, g.TenantID); err != nil {
			return checkout.State{}, err
		}
	}
	if err := s.ReturnCancelled(ctx, buyerID, id); err != nil {
		return checkout.State{}, err
	}
	return s.Get(ctx, buyerID, id)
}

// Refresh сверяет только захваченные через CAS группы; отказ также сдвигает NextPollAt.
func (s *Service) Refresh(ctx context.Context, buyerID, id ids.UUID) (checkout.State, error) {
	c, err := s.load(ctx, buyerID, id)
	if err != nil {
		return checkout.State{}, err
	}
	for _, g := range c.Groups() {
		if !g.PollDue(s.clock.Now()) {
			continue
		}
		if err := s.pollGroup(ctx, buyerID, id, g.TenantID); err != nil {
			return checkout.State{}, err
		}
	}
	if err := s.ReturnCancelled(ctx, buyerID, id); err != nil {
		return checkout.State{}, err
	}
	return s.Get(ctx, buyerID, id)
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
			if err := s.consumeCart(ctx, state.BuyerID, state.ID); err != nil {
				failures = append(failures, err)
				continue
			}
			if _, err := s.createGroups(ctx, state.BuyerID, state.ID); err != nil {
				failures = append(failures, err)
				continue
			}
			if _, err := s.Refresh(ctx, state.BuyerID, state.ID); err != nil {
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
	states, err := s.list(ctx, checkout.Filter{BuyerID: buyerID, AfterID: after, Limit: orderBatch, OrdersOnly: true})
	result := make([]order.Purchase, 0, len(states))
	for _, state := range states {
		result = append(result, order.Snapshot(state))
	}
	return result, err
}

// OperatorList читает покупки, при stuck — ожидающие дольше 15 минут.
func (s *Service) OperatorList(ctx context.Context, after ids.UUID, stuck bool) ([]checkout.State, error) {
	f := checkout.Filter{AfterID: after, Limit: orderBatch, OrdersOnly: true}
	if stuck {
		f.AwaitingBefore = s.clock.Now().Add(-15 * time.Minute)
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
func (s *Service) consumeCart(ctx context.Context, buyerID, id ids.UUID) error {
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.Checkouts().Get(ctx, id)
		if err != nil {
			return err
		}
		if c.BuyerID() != buyerID {
			return checkout.ErrNotFound
		}
		if c.State().CartConsumed {
			return nil
		}
		basket, err := tx.Carts().ByBuyer(ctx, buyerID)
		if err != nil && !errors.Is(err, cart.ErrNotFound) {
			return err
		}
		now, ttl := s.clock.Now(), s.cartTTL()
		if basket != nil && !basket.Expired(now) {
			for _, g := range c.Groups() {
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
		return tx.Checkouts().Update(ctx, c)
	})
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
			// временная проблема корзины не должна задерживать единый платёж остальных групп.
			if deferred := s.deferCartReturn(ctx, buyerID, id, g.TenantID, err); deferred != nil {
				failures = append(failures, errors.Join(err, deferred))
			}
		}
	}
	return errors.Join(failures...)
}

func (s *Service) returnCancelledGroup(ctx context.Context, buyerID, id ids.UUID, tenant ids.TenantID) error {
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.Checkouts().Get(ctx, id)
		if err != nil {
			return err
		}
		if c.BuyerID() != buyerID {
			return checkout.ErrNotFound
		}
		now, ttl := s.clock.Now(), s.cartTTL()
		for _, g := range c.Groups() {
			if g.TenantID != tenant || !g.ReturnDue(now) {
				continue
			}
			basket, err := tx.Carts().ByBuyer(ctx, buyerID)
			if errors.Is(err, cart.ErrNotFound) {
				basket, err = cart.New(s.ids.NewUUID(), buyerID, now, ttl), nil
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
			if err := tx.Carts().Save(ctx, basket); err != nil {
				return err
			}
			return tx.Checkouts().Update(ctx, c)
		}
		return nil
	})
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
