package checkout

import (
	"context"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
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
	reviewed, changed, err := s.reviewReadiness(ctx, c)
	if err != nil {
		return reviewed, err
	}
	if changed {
		return reviewed, checkout.ErrDeliveryChanged
	}
	quotes := s.groupQuotes(ctx, buyerID, c.Groups(), c.Delivery())
	for _, quote := range quotes {
		if quote.Reason == delivery.ReasonSellerUnreachable {
			return c.State(), checkout.ErrSellerUnavailable
		}
	}
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

// groupQuotes рассчитывает каждый магазин ровно один раз через предохранитель.
func (s *Service) groupQuotes(
	ctx context.Context,
	buyerID ids.UUID,
	groups []checkout.Group,
	fallback *checkout.Selection,
) map[ids.TenantID]delivery.Quote {
	return s.quoteSelections(ctx, buyerID, groups, func(g checkout.Group) *checkout.Selection {
		return fallback
	},
	)
}

func (s *Service) enqueueCreation(ctx context.Context, tx ports.Tx, c *checkout.Checkout) error {
	if c.Status() != checkout.StatusCreatingOrders {
		return nil
	}
	if err := s.consumeCart(ctx, tx, c); err != nil {
		return err
	}
	for _, g := range c.Groups() {
		if g.Excluded() || g.OrderID != "" {
			continue
		}
		if err := enqueueOrderTask(ctx, tx, TaskCreateOrder, c.State(), g, s.clock.Now()); err != nil {
			return err
		}
		if err := enqueueOrderTask(ctx, tx, TaskExpireCreation, c.State(), g, s.clock.Now()); err != nil {
			return err
		}
	}
	return nil
}
