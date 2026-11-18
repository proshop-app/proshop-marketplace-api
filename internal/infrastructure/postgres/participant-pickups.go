package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const pickupSourceTenants = publicPriceTenants + ` AND p.enabled AND `
const pickupSourceCursor = ` AND p.tenant_id > $1 ORDER BY p.tenant_id LIMIT $2`
const pickupCDEKSources = pickupSourceTenants + `(p.delivery_settings->'carriers') ? 'cdek'` + pickupSourceCursor
const pickupYandexSources = pickupSourceTenants + `(p.delivery_settings->'carriers') ? 'yandex'` + pickupSourceCursor

// PickupSources читает только id ограниченной пачки из частичного индекса службы.
func (r *Participants) PickupSources(
	ctx context.Context,
	carrier string,
	after ids.TenantID,
	limit int,
) ([]ids.TenantID, error) {
	query := pickupCDEKSources
	switch carrier {
	case "cdek":
	case "yandex":
		query = pickupYandexSources
	default:
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
