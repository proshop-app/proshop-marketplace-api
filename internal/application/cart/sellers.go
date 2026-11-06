package cart

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Sellers собирает продавцов всех строк корзины одним запросом.
func (s *Service) Sellers(ctx context.Context, state cart.State) (map[ids.TenantID]*participant.Seller, error) {
	unique := map[ids.TenantID]bool{}
	for _, line := range state.Lines {
		unique[line.Offer.Tenant] = true
	}
	result := map[ids.TenantID]*participant.Seller{}
	if len(unique) == 0 {
		return result, nil
	}
	tenants := make([]ids.TenantID, 0, len(unique))
	for tenant := range unique {
		tenants = append(tenants, tenant)
	}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		result, err = tx.Participants().Sellers(ctx, tenants)
		return err
	})
	return result, err
}
