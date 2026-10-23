package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// offerSelect — всё, из чего собирается предложение, одним запросом: снимок
// согласован, поэтому версия документа индекса монотонна.
const offerSelect = `SELECT ` + productColumns + `, ` + signalsColumns + `,
	pa.tenant_id, pa.slug, pa.name, pa.status, pa.enabled, coalesce(pa.payment_account_id, ''), pa.shop_enabled, pa.subsidy,
	pa.settings_synced_at, pa.version, pa.created_at, pa.updated_at,
	t.tenant_id, t.slug, t.status, t.cell, t.plan, t.hosts, t.event_at, t.version, t.updated_at
FROM catalog_products p
LEFT JOIN offers o ON o.tenant_id = p.tenant_id AND o.product_id = p.product_id
LEFT JOIN participants pa ON pa.tenant_id = p.tenant_id
LEFT JOIN tenants t ON t.tenant_id = p.tenant_id`

type participantRow struct {
	tenantID, slug, name, status *string
	enabled                      *bool
	paymentAccountID             *string
	shopEnabled                  *bool
	subsidy                      []byte
	syncedAt                     *time.Time
	version                      *int64
	createdAt, updatedAt         *time.Time
}

type tenantRow struct {
	tenantID, slug, status, cell *string
	plan, hosts                  []byte
	eventAt                      *time.Time
	version                      *int64
	updatedAt                    *time.Time
}

type offerRow struct {
	product     productRow
	signals     signalsRow
	participant participantRow
	tenant      tenantRow
}

func (r *offerRow) targets() []any {
	targets := append(r.product.targets(), r.signals.targets()...)

	return append(targets,
		&r.participant.tenantID, &r.participant.slug, &r.participant.name, &r.participant.status,
		&r.participant.enabled, &r.participant.paymentAccountID, &r.participant.shopEnabled, &r.participant.subsidy,
		&r.participant.syncedAt, &r.participant.version,
		&r.participant.createdAt, &r.participant.updatedAt,
		&r.tenant.tenantID, &r.tenant.slug, &r.tenant.status, &r.tenant.cell, &r.tenant.plan, &r.tenant.hosts,
		&r.tenant.eventAt, &r.tenant.version, &r.tenant.updatedAt,
	)
}

func (r *offerRow) state() (ports.OfferState, error) {
	product, err := r.product.product()
	if err != nil {
		return ports.OfferState{}, err
	}

	state := ports.OfferState{Product: product, Signals: r.signals.signals()}

	if p := r.participant; p.tenantID != nil {
		subsidy, err := decodeSubsidy(p.subsidy)
		if err != nil {
			return ports.OfferState{}, fmt.Errorf("субсидия участника %s: %w", *p.tenantID, err)
		}

		participantState := participant.State{
			TenantID: ids.TenantID(*p.tenantID), Slug: *p.slug, Name: *p.name, Status: participant.Status(*p.status),
			Enabled: *p.enabled, PaymentAccountID: *p.paymentAccountID, ShopEnabled: *p.shopEnabled, Subsidy: subsidy,
			Version: *p.version, CreatedAt: *p.createdAt, UpdatedAt: *p.updatedAt,
		}
		if p.syncedAt != nil {
			participantState.SettingsSyncedAt = *p.syncedAt
		}

		state.Participant = participant.Restore(participantState)
	}

	if t := r.tenant; t.tenantID != nil {
		state.Tenant = participant.RestoreTenant(participant.TenantState{
			TenantID: ids.TenantID(*t.tenantID), Slug: *t.slug, Status: participant.TenantStatus(*t.status),
			Cell: *t.cell, Plan: t.plan, Hosts: t.hosts, EventAt: *t.eventAt, Version: *t.version, UpdatedAt: *t.updatedAt,
		})
	}

	return state, nil
}

// Offers — чтение проекции для индекса.
type Offers struct {
	q querier
}

// Offer — снимок одного предложения.
func (r *Offers) Offer(ctx context.Context, tenantID ids.TenantID, productID ids.ProductID) (ports.OfferState, error) {
	states, err := r.query(ctx, offerSelect+` WHERE p.tenant_id = $1 AND p.product_id = $2`, string(tenantID), string(productID))
	if err != nil {
		return ports.OfferState{}, err
	}
	if len(states) == 0 {
		return ports.OfferState{}, catalog.ErrProductNotFound
	}

	return states[0], nil
}

// TenantOffers — предложения магазина по возрастанию товара.
func (r *Offers) TenantOffers(ctx context.Context, tenantID ids.TenantID, after ids.ProductID, limit int) ([]ports.OfferState, error) {
	return r.query(ctx, offerSelect+` WHERE p.tenant_id = $1 AND p.product_id > $2
		ORDER BY p.product_id LIMIT $3`, string(tenantID), string(after), limit)
}

// ParticipantOffers — предложения всех участников по возрастанию ключа.
func (r *Offers) ParticipantOffers(ctx context.Context, after ports.OfferKey, limit int) ([]ports.OfferState, error) {
	return r.query(ctx, offerSelect+` WHERE pa.tenant_id IS NOT NULL AND (p.tenant_id, p.product_id) > ($1, $2)
		ORDER BY p.tenant_id, p.product_id LIMIT $3`, string(after.TenantID), string(after.ProductID), limit)
}

// ChangedSince — предложения участников, у которых что-то менялось не раньше since.
func (r *Offers) ChangedSince(ctx context.Context, since time.Time, after ports.OfferKey, limit int) ([]ports.OfferState, error) {
	return r.query(ctx, offerSelect+` WHERE pa.tenant_id IS NOT NULL AND (p.tenant_id, p.product_id) > ($1, $2)
		AND (p.updated_at >= $3 OR o.updated_at >= $3 OR pa.updated_at >= $3 OR t.updated_at >= $3)
		ORDER BY p.tenant_id, p.product_id LIMIT $4`, string(after.TenantID), string(after.ProductID), since, limit)
}

func (r *Offers) query(ctx context.Context, sql string, args ...any) ([]ports.OfferState, error) {
	rows, err := r.q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("предложения: %w", err)
	}
	defer rows.Close()

	return collectOffers(rows)
}

func collectOffers(rows pgx.Rows) ([]ports.OfferState, error) {
	var states []ports.OfferState

	for rows.Next() {
		var row offerRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, fmt.Errorf("предложения: %w", err)
		}

		state, err := row.state()
		if err != nil {
			return nil, err
		}

		states = append(states, state)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("предложения: %w", err)
	}

	return states, nil
}
