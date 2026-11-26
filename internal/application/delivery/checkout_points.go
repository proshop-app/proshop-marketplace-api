package delivery

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
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
	checkouts ports.CheckoutBuyerReader,
	buyerID, checkoutID ids.UUID,
	query PickupQuery,
) (delivery.PickupArea, error) {
	purchase, err := checkouts.LoadForBuyer(ctx, buyerID, checkoutID)
	if err != nil {
		return delivery.PickupArea{}, err
	}
	regions := []string{}
	for _, group := range purchase.Groups() {
		if group.Abandoned {
			continue
		}
		regions = append(regions, group.Regions...)
	}
	return s.QueryFor(ctx, query.Carrier, query.City, regions, query.Bounds, query.Offset, query.Limit)
}
