package platformapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/regions"
)

const pickupCatalogRestartLimit = 2

// PickupPoints получает список по договору магазина с перевозчиком.
func (c *Client) PickupPoints(
	ctx context.Context,
	tenant ids.TenantID,
	carrier delivery.Carrier,
	city string,
) (ports.PickupPoints, error) {
	// Только справочник имеет длинный срок. Остальные HTTP-вызовы сохраняют общий потолок клиента.
	ctx, cancel := context.WithTimeout(ctx, ports.PickupCatalogBudget(c.http.Timeout))
	defer cancel()
	client := *c
	transport := *c.http
	transport.Timeout = 0 // Срок всего листания задан контекстом, а не отдельной страницы.
	client.http = &transport
	query := url.Values{"carrier": {string(carrier)}, "city": {city}}
	var collected ports.PickupPoints
	seen := make(map[string]bool)
	restarts := 0
	for {
		var answer ports.PickupPoints
		path := "/delivery/pickup-points?" + query.Encode()
		if err := client.call(ctx, http.MethodGet, tenant, path, nil, &answer); err != nil {
			var refusal *ports.OrderRefusal
			if errors.As(err, &refusal) && refusal.Code == "MARKETPLACE_PICKUP_CATALOG_CHANGED" &&
				restarts < pickupCatalogRestartLimit {
				restarts++
				collected = ports.PickupPoints{}
				seen = make(map[string]bool)
				query.Del("cursor")
				continue
			}
			return ports.PickupPoints{}, &ports.PickupCatalogFailure{Cause: err}
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
