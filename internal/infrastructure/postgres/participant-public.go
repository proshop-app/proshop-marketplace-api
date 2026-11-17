package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const publicPriceTenants = `SELECT p.tenant_id FROM participants p
    JOIN tenants t ON t.tenant_id = p.tenant_id
    WHERE p.stage = 'connected' AND p.delivery_valid AND t.status = 'active'`

const listPublicTenants = publicPriceTenants + ` AND p.tenant_id > $1 ORDER BY p.tenant_id LIMIT $2`

const selectPublicTenants = publicPriceTenants + `
    AND p.tenant_id = ANY($1::text[]) AND p.tenant_id > $2 ORDER BY p.tenant_id LIMIT $3`

// PublicTenants не читает агрегаты и не считает весь список ради одной страницы.
func (r *Participants) PublicTenants(
	ctx context.Context,
	filter []ids.TenantID,
	after ids.TenantID,
	limit int,
) ([]ids.TenantID, error) {
	query, args := listPublicTenants, []any{string(after), limit}
	if filter != nil {
		keys := make([]string, len(filter))
		for i, tenant := range filter {
			keys[i] = string(tenant)
		}
		query, args = selectPublicTenants, []any{keys, string(after), limit}
	}
	rows, err := rawSQL(ctx, r.db, query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
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
