package postgres

import (
	"context"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const selectParticipantSellers = `SELECT tenant_id, slug, name FROM participants WHERE tenant_id = ANY($1::text[])`

// Sellers возвращает имена всей пачки без загрузки агрегатов.
func (r *Participants) Sellers(
	ctx context.Context,
	tenants []ids.TenantID,
) (map[ids.TenantID]*participant.Seller, error) {
	result := make(map[ids.TenantID]*participant.Seller)
	if len(tenants) == 0 {
		return result, nil
	}
	rows, err := r.q.Query(ctx, selectParticipantSellers, tenants)
	if err != nil {
		return nil, fmt.Errorf("продавцы: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tenant ids.TenantID
		var seller participant.Seller
		if err := rows.Scan(&tenant, &seller.Slug, &seller.Name); err != nil {
			return nil, err
		}
		result[tenant] = &seller
	}
	return result, rows.Err()
}
