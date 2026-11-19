package checkout

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Сначала фиксируем необходимость согласия, затем делаем один расчёт вне транзакции.
func (s *Service) reviewDispatch(
	ctx context.Context,
	buyerID, id ids.UUID,
	group checkout.Group,
	token string,
	revision int,
	fallback *checkout.Selection,
) error {
	changed := false
	state, err := s.retrySelection(ctx, buyerID, id, nil, func(c *checkout.Checkout, now time.Time) error {
		var err error
		changed, err = c.BeginDispatchReview(group.TenantID, token, revision, now)
		return err
	})
	if err != nil || !changed {
		return err
	}
	for _, current := range state.Groups {
		if current.TenantID == group.TenantID {
			group = current
		}
	}
	quote := s.quoteGroup(ctx, buyerID, group, *fallback)
	after := func(ctx context.Context, tx ports.Tx, c *checkout.Checkout) error {
		if err := s.enqueueCancellations(ctx, tx, c); err != nil {
			return err
		}
		return s.returnItems(ctx, tx, c)
	}
	_, err = s.retrySelection(ctx, buyerID, id, after, func(c *checkout.Checkout, now time.Time) error {
		return c.ApplyDispatchQuote(group.TenantID, quote, now)
	})
	return err
}
