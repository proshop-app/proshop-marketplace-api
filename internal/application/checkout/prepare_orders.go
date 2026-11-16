package checkout

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CreationTimeout — обещанный покупателю срок от фиксации намерения.
const CreationTimeout = 15 * time.Second

func (s *Service) prepareOrders(
	ctx context.Context,
	buyerID, id ids.UUID,
	contact checkout.Contact,
) (checkout.State, error) {
	c, err := s.load(ctx, buyerID, id)
	if err != nil {
		return checkout.State{}, err
	}
	if c.Status() != checkout.StatusDraft && c.Status() != checkout.StatusConfirmed {
		return c.State(), nil
	}
	if !s.config.PaymentStub {
		return checkout.State{}, checkout.ErrPaymentUnavailable
	}
	if c.Status() == checkout.StatusDraft {
		return c.State(), checkout.ErrTransition
	}
	quotes := s.freshQuotes(ctx, buyerID, c.Groups(), *c.Delivery())
	expected := c.State().Version
	deliveryChanged := false
	begin := func(current *checkout.Checkout, now time.Time) error {
		if current.State().Version != expected {
			return checkout.ErrConflict
		}
		var err error
		deliveryChanged, err = current.RecheckDelivery(quotes, now)
		if err != nil || deliveryChanged {
			return err
		}
		if _, err = current.BeginOrders(contact, now); err != nil {
			return err
		}
		return current.SetCreationDeadline(now.Add(CreationTimeout), now)
	}
	state, err := s.updateSelection(ctx, buyerID, id, s.enqueueCreation, begin)
	if errors.Is(err, checkout.ErrConflict) || errors.Is(err, cart.ErrConflict) {
		current, loadErr := s.load(ctx, buyerID, id)
		if loadErr == nil && current.Status() != checkout.StatusConfirmed && current.Status() != checkout.StatusDraft {
			return current.State(), nil
		}
	}
	if err != nil {
		return state, err
	}
	if deliveryChanged {
		return state, checkout.ErrDeliveryChanged
	}
	return state, nil
}

// freshQuotes всегда читает настройки сервера, даже если локальная проекция и quote ещё свежие.
func (s *Service) freshQuotes(
	ctx context.Context,
	buyerID ids.UUID,
	groups []checkout.Group,
	selection checkout.Selection,
) map[ids.TenantID]delivery.Quote {
	quotes := make(map[ids.TenantID]delivery.Quote, len(groups))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, group := range groups {
		wg.Add(1)
		go func(g checkout.Group) {
			defer wg.Done()
			q := s.freshGroupQuote(ctx, buyerID, g, selection)
			mu.Lock()
			quotes[g.TenantID] = q
			mu.Unlock()
		}(group)
	}
	wg.Wait()
	return quotes
}

func (s *Service) freshGroupQuote(
	ctx context.Context,
	buyerID ids.UUID,
	group checkout.Group,
	selection checkout.Selection,
) delivery.Quote {
	callCtx, cancel := context.WithTimeout(ctx, s.config.QuoteTimeout)
	defer cancel()
	settings, err := s.platform.MarketplaceSettings(callCtx, group.TenantID)
	if err != nil {
		return delivery.Unreachable(s.clock.Now())
	}
	if settings.Delivery.Validate() != nil || !settings.Subsidy.Enabled ||
		settings.Subsidy.Validate() != nil || settings.FreeDeliveryFrom.IsNegative() {
		return delivery.Quote{
			Status:    delivery.QuoteUnavailable,
			Reason:    delivery.ReasonPickupPointUnavailable,
			ExpiresAt: s.clock.Now().Add(s.config.QuoteTTL),
		}
	}
	return s.quoteGroup(callCtx, buyerID, group, selection, participant.State{
		FreeDeliveryFrom: settings.FreeDeliveryFrom,
		Delivery:         settings.Delivery,
		Subsidy:          settings.Subsidy,
	})
}

func (s *Service) enqueueCreation(ctx context.Context, tx ports.Tx, c *checkout.Checkout) error {
	if c.Status() != checkout.StatusCreatingOrders || c.State().CartConsumed {
		return nil
	}
	if err := s.consumeCart(ctx, tx, c); err != nil {
		return err
	}
	for _, g := range c.Groups() {
		if err := enqueueOrderTask(ctx, tx, TaskCreateOrder, c.State(), g, s.clock.Now()); err != nil {
			return err
		}
		if err := enqueueOrderTask(ctx, tx, TaskExpireCreation, c.State(), g, s.clock.Now()); err != nil {
			return err
		}
	}
	return nil
}
