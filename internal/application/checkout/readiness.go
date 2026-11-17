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

// ready читает те же входы ComposeOffer одной пачкой: индекс может ещё не получить отзыв готовности.
func (s *Service) ready(ctx context.Context, groups []checkout.Group) error {
	keys := make([]ids.OfferID, 0)
	for _, group := range groups {
		if group.CreateExhausted || (group.Quote != nil && group.Quote.Status == delivery.QuoteUnavailable) {
			continue
		}
		for _, line := range group.Lines {
			keys = append(keys, line.Offer)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		states, err := tx.Offers().Batch(ctx, keys)
		if err != nil {
			return err
		}
		if len(states) != len(keys) {
			return checkout.ErrOfferUnavailable
		}
		for _, state := range states {
			visibility := participant.VisibilityOf(state.Participant, state.Tenant)
			seller := catalog.Seller{Visible: visibility.Visible, CheckoutAvailable: visibility.CheckoutAvailable}
			offer := catalog.ComposeOffer(state.Product, state.Signals, seller, state.Verdict)
			if !offer.CheckoutAvailable {
				message := "предложение не готово к оформлению: " + offer.ID.String()
				return checkout.ErrOfferUnavailable.WithMessage(message)
			}
		}
		return nil
	})
}
