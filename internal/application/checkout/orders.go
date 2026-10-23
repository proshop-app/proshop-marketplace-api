package checkout

import (
	"context"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/order"
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
	buyerID, checkoutID ids.UUID,
	contact checkout.Contact,
) (checkout.State, error) {
	returnURL := s.config.ReturnBaseURL + "/api/v1/checkout/" + string(checkoutID) + "/return"
	_, err := s.update(ctx, buyerID, checkoutID, func(c *checkout.Checkout, now time.Time) error {
		_, err := c.BeginOrders(contact, returnURL, now)
		return err
	})
	if err != nil {
		return checkout.State{}, err
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
		if g.Status != checkout.GroupCreating && g.Status != checkout.GroupSellerUnreachable {
			continue
		}
		state := c.State()
		request := ports.CreateOrderRequest{
			ExternalCheckoutID: g.ExternalCheckoutID,
			GroupID:            g.TenantID,
			Carrier:            string(state.Delivery.Carrier),
			PickupPointCode:    state.Delivery.PickupPointCode,
			Contact:            state.Contact,
			DeliveryPrice:      g.DeliveryPrice,
			ReturnURL:          state.ReturnURL,
		}
		for _, line := range g.Lines {
			request.Items = append(request.Items, ports.OrderItem{
				ProductID: line.Offer.Product,
				Quantity:  line.Quantity,
				Price:     line.Price,
			})
		}
		answer, failed := s.platform.CreateOrder(ctx, g.TenantID, request)
		_, err = s.update(ctx, buyerID, id, func(current *checkout.Checkout, now time.Time) error {
			if failed == nil {
				_, err := current.AttachOrder(g.TenantID, answer, now)
				if errors.Is(err, checkout.ErrInvalidOrder) {
					_, err = current.CreationFailed(g.TenantID, nil, false, now)
				}
				return err
			}
			var changed *ports.PriceChanged
			var prices []checkout.PriceChange
			if errors.As(failed, &changed) {
				prices = changed.Items
			}
			_, err := current.CreationFailed(g.TenantID, prices, errors.Is(failed, ports.ErrPriceChanged), now)
			return err
		})
		if err != nil {
			return checkout.State{}, err
		}
	}
	if err := s.ReturnCancelled(ctx, buyerID, id); err != nil {
		return checkout.State{}, err
	}
	return s.Get(ctx, buyerID, id)
}

// Refresh опрашивает магазины; просроченные группы продолжают сверяться до подтверждённой отмены.
func (s *Service) Refresh(ctx context.Context, buyerID, id ids.UUID) (checkout.State, error) {
	c, err := s.load(ctx, buyerID, id)
	if err != nil {
		return checkout.State{}, err
	}
	for _, g := range c.Groups() {
		if g.OrderID == "" || g.Status == checkout.GroupCancelled || g.Status == checkout.GroupReturned {
			continue
		}
		if s.clock.Now().Before(g.NextPollAt) {
			continue
		}
		answer, err := s.platform.GetOrder(ctx, g.TenantID, g.OrderID)
		if err != nil {
			continue
		}
		interval := paidPoll
		if answer.Status == checkout.GroupAwaitingPayment {
			interval = unpaidPoll
		}
		_, err = s.update(ctx, buyerID, id, func(current *checkout.Checkout, now time.Time) error {
			_, err := current.Observe(g.TenantID, answer, now.Add(interval), now)
			return err
		})
		if err != nil {
			return checkout.State{}, err
		}
	}
	if err := s.ReturnCancelled(ctx, buyerID, id); err != nil {
		return checkout.State{}, err
	}
	return s.Get(ctx, buyerID, id)
}

// Pay сверяет группу и возвращает ссылку владельцу до истечения окна.
func (s *Service) Pay(ctx context.Context, buyerID, id ids.UUID, tenant ids.TenantID) (*checkout.PaymentLink, error) {
	state, err := s.Refresh(ctx, buyerID, id)
	if err != nil {
		return nil, err
	}
	return checkout.Restore(state).Payment(tenant, s.clock.Now())
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

// ReturnCancelled атомарно возвращает отменённые группы и отмечает возврат в покупке.
func (s *Service) ReturnCancelled(ctx context.Context, buyerID, id ids.UUID) error {
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.Checkouts().Get(ctx, id)
		if err != nil {
			return err
		}
		if c.BuyerID() != buyerID {
			return checkout.ErrNotFound
		}
		now, ttl := s.clock.Now(), s.cartTTL()
		var basket *cart.Cart
		changed := false
		for _, g := range c.Groups() {
			if g.CartReturned || (g.Status != checkout.GroupCancelled && g.Status != checkout.GroupPriceChanged) {
				continue
			}
			if basket == nil {
				basket, err = tx.Carts().ByBuyer(ctx, buyerID)
				if errors.Is(err, cart.ErrNotFound) {
					basket, err = cart.New(s.ids.NewUUID(), buyerID, now, ttl), nil
				}
				if err != nil {
					return err
				}
				basket.ResetIfExpired(now, ttl)
			}
			for _, line := range g.Lines {
				if err := basket.Return(line.Offer, line.Name, line.Price, line.Quantity, now, ttl); err != nil {
					return err
				}
			}
			if _, err := c.MarkCartReturned(g.TenantID, now); err != nil {
				return err
			}
			changed = true
		}
		if !changed {
			return nil
		}
		if err := tx.Carts().Save(ctx, basket); err != nil {
			return err
		}
		return tx.Checkouts().Update(ctx, c)
	})
}

func (s *Service) cartTTL() time.Duration {
	if s.config.CartTTL > 0 {
		return s.config.CartTTL
	}
	return 30 * 24 * time.Hour
}

// NextPayment выбирает ссылку относительно часов приложения.
func (s *Service) NextPayment(state checkout.State) *checkout.PaymentLink {
	return checkout.Restore(state).NextPayment(s.clock.Now())
}
