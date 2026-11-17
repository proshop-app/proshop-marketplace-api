package platformapi

import (
	"context"
	"net/http"
	"net/url"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/regions"
)

// PickupPoints получает список по договору магазина с перевозчиком.
func (c *Client) PickupPoints(
	ctx context.Context,
	tenant ids.TenantID,
	carrier delivery.Carrier,
	city string,
) (ports.PickupPoints, error) {
	query := url.Values{"carrier": {string(carrier)}, "city": {city}}
	var answer ports.PickupPoints
	path := "/delivery/pickup-points?" + query.Encode()
	if err := c.call(ctx, http.MethodGet, tenant, path, nil, &answer); err != nil {
		return ports.PickupPoints{}, err
	}
	if !answer.Available && answer.Reason != delivery.ReasonCarrierNotConnected {
		return ports.PickupPoints{}, ErrInvalidResponse
	}
	if answer.Available && answer.Reason != "" {
		return ports.PickupPoints{}, ErrInvalidResponse
	}
	for _, point := range answer.Items {
		_, err := delivery.NewPickupPoint(
			point.Carrier,
			point.Code,
			point.Name,
			point.Address,
			point.City,
			point.Location,
			point.Schedule,
		)
		if err != nil {
			return ports.PickupPoints{}, ErrInvalidResponse.Wrap(err)
		}
		if _, err := delivery.NormalizeCity(point.City); err != nil {
			return ports.PickupPoints{}, ErrInvalidResponse.Wrap(err)
		}
		if point.Carrier != carrier || !regions.ValidRU(point.RegionCode) {
			return ports.PickupPoints{}, ErrInvalidResponse
		}
	}
	return answer, nil
}
