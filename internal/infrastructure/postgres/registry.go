package postgres

import (
	"context"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	selectTenant = `
		SELECT tenant_id, slug, status, cell, plan, hosts, event_at, version, updated_at
		FROM tenants
		WHERE tenant_id = $1`

	insertTenant = `
		INSERT INTO tenants (tenant_id, slug, status, cell, plan, hosts, event_at, version, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	updateTenant = `
		UPDATE tenants
		SET slug = $2,
			status = $3,
			cell = $4,
			plan = $5,
			hosts = $6,
			event_at = $7,
			version = $8,
			updated_at = $9
		WHERE tenant_id = $1 AND version = $10`
)

// Registry — зеркало реестра магазинов в PostgreSQL.
type Registry struct {
	q querier
}

// Get — запись реестра.
func (r *Registry) Get(ctx context.Context, tenantID ids.TenantID) (*participant.Tenant, error) {
	var (
		state  participant.TenantState
		id     string
		status string
		plan   []byte
		hosts  []byte
	)

	err := r.q.QueryRow(ctx, selectTenant, string(tenantID)).Scan(
		&id,
		&state.Slug,
		&status,
		&state.Cell,
		&plan,
		&hosts,
		&state.EventAt,
		&state.Version,
		&state.UpdatedAt,
	)
	if notFound(err) {
		return nil, participant.ErrTenantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("запись реестра %s: %w", tenantID, err)
	}

	state.TenantID = ids.TenantID(id)
	state.Status = participant.TenantStatus(status)
	state.Plan = plan
	state.Hosts = hosts

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
	}

	if tenant.OriginVersion() == 0 {
		_, err := r.q.Exec(ctx, insertTenant, args...)
		if isUnique(err, "tenants_pkey") {
			return participant.ErrTenantConflict
		}
		if err != nil {
			return fmt.Errorf("новая запись реестра %s: %w", s.TenantID, err)
		}

		tenant.MarkSaved()

		return nil
	}

	tag, err := r.q.Exec(ctx, updateTenant, append(args, tenant.OriginVersion())...)
	if err != nil {
		return fmt.Errorf("запись реестра %s: %w", s.TenantID, err)
	}
	if tag.RowsAffected() == 0 {
		return participant.ErrTenantConflict
	}

	tenant.MarkSaved()

	return nil
}
