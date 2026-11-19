package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const selectParticipantSellers = `SELECT p.tenant_id, p.slug, p.name, p.stage, p.delivery_settings,
    coalesce(t.status, '') FROM participants p LEFT JOIN tenants t ON t.tenant_id = p.tenant_id
    WHERE p.tenant_id = ANY($1::text[])`

// Sellers возвращает имена всей пачки без загрузки агрегатов.
func (r *Participants) Sellers(
	ctx context.Context,
	tenants []ids.TenantID,
) (map[ids.TenantID]*participant.Seller, error) {
	result := make(map[ids.TenantID]*participant.Seller)
	if len(tenants) == 0 {
		return result, nil
	}
	rows, err := rawSQL(ctx, r.db, selectParticipantSellers, tenants).Rows()
	if err != nil {
		return nil, fmt.Errorf("продавцы: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var tenant ids.TenantID
		var state participant.State
		var status participant.TenantStatus
		var settings []byte
		if err := rows.Scan(&tenant, &state.Slug, &state.Name, &state.Stage, &settings, &status); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(settings, &state.Delivery); err != nil {
			return nil, err
		}
		state.TenantID = tenant
		record := participant.RestoreTenant(participant.TenantState{TenantID: tenant, Status: status})
		result[tenant] = participant.SellerOf(participant.Restore(state), record)
	}
	return result, rows.Err()
}
