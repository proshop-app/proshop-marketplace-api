package checkout

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

// reviewReadiness возвращает новый состав до подтверждения или начала оплаты.
func (s *Service) reviewReadiness(
	ctx context.Context,
	c *checkout.Checkout,
) (checkout.State, bool, error) {
	reasons, err := s.unready(ctx, c.Groups())
	final := false
	for _, reason := range reasons {
		final = final || !checkout.PaymentHoldReason(reason)
	}
	if err != nil || !final {
		return c.PaymentView(reasons, s.clock.Now()), false, err
	}
	expected := c.State().Version
	changed := false
	after := func(ctx context.Context, tx ports.Tx, current *checkout.Checkout) error {
		if err := s.enqueueCancellations(ctx, tx, current); err != nil {
			return err
		}
		return s.returnItems(ctx, tx, current)
	}
	mutate := func(current *checkout.Checkout, now time.Time) error {
		if current.State().Version != expected {
			return checkout.ErrConflict
		}
		var err error
		changed, err = current.ReviewGroups(reasons, now)
		return err
	}
	state, err := s.updateSelection(ctx, c.BuyerID(), c.ID(), after, mutate)
	return checkout.Restore(state).PaymentView(reasons, s.clock.Now()), changed, err
}
