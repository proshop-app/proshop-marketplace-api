package checkout

import (
	"context"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (s *Service) prepareOrders(
	ctx context.Context,
	buyerID, id ids.UUID,
	contact checkout.Contact,
) (checkout.State, error) {
	c, err := s.load(ctx, buyerID, id)
	if err != nil {
		return checkout.State{}, err
	}
	if c.Status() == checkout.StatusDraft {
		return c.State(), checkout.ErrTransition
	}
	if c.Status() != checkout.StatusConfirmed {
		return c.State(), nil
	}
	quotes := s.freshQuotes(ctx, buyerID, c.Groups(), *c.Delivery())
	expected := c.State().Version
	returnURL := s.config.ReturnBaseURL + "/api/v1/checkout/" + string(id) + "/return"
	deliveryChanged := false
	state, err := s.update(ctx, buyerID, id, func(current *checkout.Checkout, now time.Time) error {
		if current.State().Version != expected {
			return checkout.ErrConflict
		}
		var err error
		deliveryChanged, err = current.RecheckDelivery(quotes, now)
		if err != nil || deliveryChanged {
			return err
		}
		_, err = current.BeginOrders(contact, returnURL, now)
		return err
	})
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
