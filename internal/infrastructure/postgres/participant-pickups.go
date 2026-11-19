package postgres

import (
	"context"
	"slices"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const pickupSourceTenants = publicPriceTenants + ` AND p.enabled `
const pickupSourceCursor = ` AND p.tenant_id > $1 ORDER BY p.tenant_id LIMIT $2`
const pickupVisibleSources = pickupSourceTenants + pickupSourceCursor

// PickupSources читает только id ограниченной пачки из частичного индекса видимых магазинов.
func (r *Participants) PickupSources(
	ctx context.Context,
	carrier string,
	after ids.TenantID,
	limit int,
) ([]ids.TenantID, error) {
	query := pickupVisibleSources
	if !slices.ContainsFunc(delivery.Carriers(), func(item delivery.CarrierInfo) bool {
		return string(item.Code) == carrier
	}) {
		return nil, delivery.ErrInvalidCarrier
	}
	rows, err := rawSQL(ctx, r.db, query, string(after), limit).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []ids.TenantID{}
	for rows.Next() {
		var tenant ids.TenantID
		if err := rows.Scan(&tenant); err != nil {
			return nil, err
		}
		result = append(result, tenant)
	}
	return result, rows.Err()
}
