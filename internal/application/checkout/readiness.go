package checkout

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ready применяется там, где вся новая покупка ещё может быть отклонена целиком.
func (s *Service) ready(ctx context.Context, groups []checkout.Group) error {
	reasons, err := s.unready(ctx, groups)
	if err != nil {
		return err
	}
	if len(reasons) > 0 {
		return checkout.ErrOfferUnavailable
	}
	return nil
}

// unready читает все входы готовности одной пачкой, не вызывая магазины.
func (s *Service) unready(ctx context.Context, groups []checkout.Group) (map[ids.TenantID]string, error) {
	keys := []ids.OfferID{}
	for _, group := range groups {
		if group.Excluded() || group.Paid() {
			continue
		}
		for _, line := range group.Lines {
			keys = append(keys, line.Offer)
		}
	}
	reasons := make(map[ids.TenantID]string)
	if len(keys) == 0 {
		return reasons, nil
	}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		states, err := tx.Offers().Batch(ctx, keys)
		if err != nil {
			return err
		}
		ready := make(map[ids.OfferID]bool, len(states))
		for _, state := range states {
			visibility := participant.VisibilityOf(state.Participant, state.Tenant)
			seller := catalog.Seller{Visible: visibility.Visible, CheckoutAvailable: visibility.CheckoutAvailable}
			offer := catalog.ComposeOffer(state.Product, state.Signals, seller, state.Verdict)
			ready[offer.ID] = offer.CheckoutAvailable
		}
		for _, key := range keys {
			if !ready[key] {
				reasons[key.Tenant] = delivery.ReasonSellerNotReady
			}
		}
		return nil
	},
	)
	return reasons, err
}
