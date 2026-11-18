package checkout

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// RemoveGroup возвращает товары и ставит отмену по ключу в той же транзакции, что новое состояние покупки.
func (s *Service) RemoveGroup(ctx context.Context, buyerID, id ids.UUID, tenant ids.TenantID) (checkout.State, error) {
	after := func(ctx context.Context, tx ports.Tx, c *checkout.Checkout) error {
		if err := s.returnItems(ctx, tx, c); err != nil {
			return err
		}
		return s.enqueueCancellations(ctx, tx, c)
	}
	return s.retrySelection(ctx, buyerID, id, after, func(c *checkout.Checkout, now time.Time) error {
		_, err := c.RemoveGroup(tenant, now)
		return err
	})
}
