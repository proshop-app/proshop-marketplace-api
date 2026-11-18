package delivery

import (
	"context"
	"slices"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// PickupQuery задаёт область карты либо страницу списка.
type PickupQuery struct {
	Carrier delivery.Carrier
	City    string
	Bounds  *delivery.Bounds
	Offset  int
	Limit   int
}

// Query возвращает ограниченный публичный справочник.
func (s *Service) Query(ctx context.Context, query PickupQuery) (delivery.PickupArea, error) {
	return s.QueryFor(ctx, query.Carrier, query.City, nil, query.Bounds, query.Offset, query.Limit)
}

// CheckoutPoints проверяет владельца и применяет разрешения групп одним сценарием.
func (s *Service) CheckoutPoints(
	ctx context.Context,
	buyerID, checkoutID ids.UUID,
	tenant ids.TenantID,
	query PickupQuery,
) (delivery.PickupArea, error) {
	var purchase *checkout.Checkout
	err := s.uow.Do(
		ctx,
		func(ctx context.Context, tx ports.Tx) error {
			var err error
			purchase, err = tx.Checkouts().Get(ctx, checkoutID)
			return err
		},
	)
	if err != nil {
		return delivery.PickupArea{}, err
	}
	if purchase.BuyerID() != buyerID {
		return delivery.PickupArea{}, checkout.ErrNotFound
	}
	tenants := []ids.TenantID{}
	regions := []string{}
	for _, group := range purchase.Groups() {
		if tenant != "" && group.TenantID != tenant {
			continue
		}
		tenants = append(tenants, group.TenantID)
		if slices.Contains(group.Carriers, string(query.Carrier)) {
			regions = append(regions, group.Regions...)
		}
	}
	if len(tenants) == 0 {
		return delivery.PickupArea{}, checkout.ErrNotFound
	}
	ctx = ports.WithPlatformCallClass(ctx, ports.PlatformCheckout)
	return s.QueryFor(ctx, query.Carrier, query.City, regions, query.Bounds, query.Offset, query.Limit)
}
