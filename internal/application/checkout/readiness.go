package checkout

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
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
		unavailable := make(map[ids.OfferID]string, len(states))
		for _, state := range states {
			visibility := participant.VisibilityOf(state.Participant, state.Tenant)
			seller := catalog.Seller{Visible: visibility.Visible, CheckoutAvailable: visibility.CheckoutAvailable}
			offer := catalog.ComposeOffer(state.Product, state.Signals, seller, state.Verdict)
			unavailable[offer.ID] = ""
			if !offer.CheckoutAvailable {
				unavailable[offer.ID] = unavailableReason(state, visibility.CheckoutAvailable)
			}
		}
		for _, group := range groups {
			if group.Excluded() || group.Paid() {
				continue
			}
			for _, line := range group.Lines {
				reason, found := unavailable[line.Offer]
				if found && reason == "" {
					continue
				}
				if !found {
					reason = delivery.ReasonSellerNotReady
				}
				if group.OrderID == "" || group.EffectiveStatus() != checkout.GroupAwaitingPayment ||
					!checkout.PaymentHoldReason(reason) {
					reasons[group.TenantID] = delivery.ReasonSellerNotReady
					break
				}
				reasons[group.TenantID] = reason
			}
		}
		return nil
	},
	)
	return reasons, err
}

// Временное отсутствие карточки не отменяет уже созданный неоплаченный заказ.
func unavailableReason(state ports.OfferState, sellerReady bool) string {
	if state.Product == nil || !sellerReady ||
		(state.Verdict != nil && state.Verdict.State().Status == moderation.Rejected) {
		return delivery.ReasonSellerNotReady
	}
	product := state.Product.State()
	if product.Deleted {
		return delivery.ReasonSellerNotReady
	}
	if state.Verdict != nil && state.Verdict.State().Status == moderation.Pending {
		return checkout.HoldModerationPending
	}
	if !product.Card.Visible || !product.Card.Publication.Published {
		return checkout.HoldPublicationPaused
	}
	return delivery.ReasonSellerNotReady
}

func (s *Service) paymentView(ctx context.Context, c *checkout.Checkout) (checkout.State, error) {
	for _, group := range c.Groups() {
		if group.OrderID != "" && group.EffectiveStatus() == checkout.GroupAwaitingPayment {
			reasons, err := s.unready(ctx, c.Groups())
			return c.PaymentView(reasons, s.clock.Now()), err
		}
	}
	return c.State(), nil
}
