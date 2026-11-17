package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const batchTenants = `SELECT tenant_id, slug, status, cell, plan, hosts, event_at, version, updated_at, name
    FROM tenants WHERE tenant_id = ANY($1::text[])`

// Batch возвращает согласованный набор записей реестра для страницы участников.
func (r *Registry) Batch(ctx context.Context, keys []ids.TenantID) (map[ids.TenantID]*participant.Tenant, error) {
	result := map[ids.TenantID]*participant.Tenant{}
	if len(keys) == 0 {
		return result, nil
	}
	rows, err := rawSQL(ctx, r.db, batchTenants, keys).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		tenant, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		result[tenant.ID()] = tenant
	}
	return result, rows.Err()
}
