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
	query := url.Values{"carrier": {string(carrier)}, "city": {city}, "page": {"1"}}
	var collected ports.PickupPoints
	seen := make(map[string]bool)
	for {
		var answer ports.PickupPoints
		path := "/delivery/pickup-points?" + query.Encode()
		if err := c.call(ctx, http.MethodGet, tenant, path, nil, &answer); err != nil {
			return ports.PickupPoints{}, err
		}
		if err := validatePickupPage(answer, carrier); err != nil {
			return ports.PickupPoints{}, err
		}
		if !answer.Available {
			if len(collected.Items) > 0 {
				return ports.PickupPoints{}, ErrUnavailable
			}
			return answer, nil
		}
		collected.Available = true
		collected.Items = append(collected.Items, answer.Items...)
		if answer.NextCursor == nil {
			if answer.Total != nil && len(collected.Items) != *answer.Total {
				return ports.PickupPoints{}, ErrInvalidResponse
			}
			return collected, nil
		}
		cursor := *answer.NextCursor
		if cursor == "" || seen[cursor] || len(answer.Items) == 0 {
			return ports.PickupPoints{}, ErrInvalidResponse
		}
		seen[cursor] = true
		query.Set("cursor", cursor)
	}
}

func validatePickupPage(answer ports.PickupPoints, carrier delivery.Carrier) error {
	if !answer.Available && answer.Reason != delivery.ReasonCarrierNotConnected {
		return ErrInvalidResponse
	}
	if answer.Available && answer.Reason != "" {
		return ErrInvalidResponse
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
			return ErrInvalidResponse.Wrap(err)
		}
		if _, err := delivery.NormalizeCity(point.City); err != nil {
			return ErrInvalidResponse.Wrap(err)
		}
		if point.Carrier != carrier || !regions.ValidRU(point.RegionCode) {
			return ErrInvalidResponse
		}
	}
	return nil
}
