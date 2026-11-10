package postgres

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	listActiveTenants = `
		SELECT tenant_id, slug, status, cell, plan, hosts, event_at, version, updated_at, name
		FROM tenants WHERE status = 'active' AND tenant_id > $1
		ORDER BY tenant_id LIMIT $2`
	selectTenant = `
		SELECT tenant_id, slug, status, cell, plan, hosts, event_at, version, updated_at, name
		FROM tenants
		WHERE tenant_id = $1`

	insertTenant = `
		INSERT INTO tenants (tenant_id, slug, status, cell, plan, hosts, event_at, version, updated_at, name)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	updateTenant = `
		UPDATE tenants
		SET slug = $2,
			status = $3,
			cell = $4,
			plan = $5,
			hosts = $6,
			event_at = $7,
			version = $8,
			updated_at = $9,
			name = $10
		WHERE tenant_id = $1 AND version = $11`
)

// Registry — зеркало реестра магазинов в PostgreSQL.
type Registry struct {
	db *gorm.DB
}

// Get — запись реестра.
func (r *Registry) Get(ctx context.Context, tenantID ids.TenantID) (*participant.Tenant, error) {
	tenant, err := scanTenant(rawSQL(ctx, r.db, selectTenant, string(tenantID)).Row())
	if notFound(err) {
		return nil, participant.ErrTenantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("запись реестра %s: %w", tenantID, err)
	}
	return tenant, nil
}

func scanTenant(row rowScanner) (*participant.Tenant, error) {
	var state participant.TenantState
	var plan, hosts []byte

	err := row.Scan(
		&state.TenantID,
		&state.Slug,
		&state.Status,
		&state.Cell,
		&plan,
		&hosts,
		&state.EventAt,
		&state.Version,
		&state.UpdatedAt,
		&state.Name,
	)
	if err != nil {
		return nil, err
	}
	state.Plan, state.Hosts = plan, hosts

	return participant.RestoreTenant(state), nil
}

// Save заводит запись или обновляет её условно по версии.
func (r *Registry) Save(ctx context.Context, tenant *participant.Tenant) error {
	s := tenant.State()
	args := []any{
		string(s.TenantID),
		s.Slug,
		string(s.Status),
		s.Cell,
		jsonArg(s.Plan),
		jsonArg(s.Hosts),
		s.EventAt,
		s.Version,
		s.UpdatedAt,
		s.Name,
	}

	if tenant.OriginVersion() == 0 {
		_, err := execSQL(ctx, r.db, insertTenant, args...)
		if isUnique(err, "tenants_pkey") {
			return participant.ErrTenantConflict
		}
		if err != nil {
			return fmt.Errorf("новая запись реестра %s: %w", s.TenantID, err)
		}

		tenant.MarkSaved()

		return nil
	}

	affected, err := execSQL(ctx, r.db, updateTenant, append(args, tenant.OriginVersion())...)
	if err != nil {
		return fmt.Errorf("запись реестра %s: %w", s.TenantID, err)
	}
	if affected == 0 {
		return participant.ErrTenantConflict
	}

	tenant.MarkSaved()

	return nil
}

// List обходит активный реестр по устойчивому ключу.
func (r *Registry) List(ctx context.Context, after ids.TenantID, limit int) ([]*participant.Tenant, error) {
	rows, err := rawSQL(ctx, r.db, listActiveTenants, after, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := []*participant.Tenant{}
	for rows.Next() {
		tenant, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, tenant)
	}
	return result, rows.Err()
}
