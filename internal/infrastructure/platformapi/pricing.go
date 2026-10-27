package platformapi

import (
	"context"
	"net/http"
	"regexp"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

var priceProfilePattern = regexp.MustCompile(`^marketplace:(default|[a-f0-9]{24})$`)

type priceContextJSON struct {
	TenantID   ids.TenantID `json:"tenantId"`
	ProfileKey string       `json:"profileKey"`
	Revision   int64        `json:"revision"`
}

// PriceContexts получает выбор сервера, не прайс или персональные данные покупателя.
func (c *Client) PriceContexts(
	ctx context.Context,
	buyerID ids.UUID,
	tenants []ids.TenantID,
) (map[ids.TenantID]string, error) {
	var answer struct {
		Items []priceContextJSON `json:"items"`
	}
	body := map[string]any{
		"buyerId":   buyerID,
		"tenantIds": tenants,
	}
	if err := c.call(ctx, http.MethodPost, "", "/pricing/contexts", body, &answer); err != nil {
		return nil, err
	}
	result := make(map[ids.TenantID]string, len(tenants))
	allowed := make(map[ids.TenantID]bool, len(tenants))
	for _, tenant := range tenants {
		allowed[tenant] = true
	}
	for _, item := range answer.Items {
		if !allowed[item.TenantID] || !priceProfilePattern.MatchString(item.ProfileKey) ||
			result[item.TenantID] != "" || item.Revision < 0 {
			return nil, ErrInvalidResponse
		}
		result[item.TenantID] = item.ProfileKey
	}
	if len(result) != len(allowed) {
		return nil, ErrInvalidResponse
	}
	return result, nil
}
