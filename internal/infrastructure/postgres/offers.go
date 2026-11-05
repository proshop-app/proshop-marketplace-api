package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"

	"github.com/jackc/pgx/v5"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	// offerSelect — всё, из чего собирается предложение, одним запросом: снимок
	// согласован, поэтому версия документа индекса монотонна.
	offerSelect = `
		SELECT ` + productColumns + `, ` + signalsColumns + `,
			pa.tenant_id, pa.slug, pa.name, pa.status, pa.enabled,
			pa.shop_enabled, pa.subsidy, pa.settings_synced_at, pa.version, pa.created_at, pa.updated_at,
			pa.free_delivery_from, pa.delivery_settings, pa.trust, pa.approved_offers,
			t.tenant_id, t.slug, t.status, t.cell, t.plan, t.hosts, t.event_at, t.version, t.updated_at,
			to_jsonb(mv)
		FROM catalog_products p
		LEFT JOIN offers o ON o.tenant_id = p.tenant_id AND o.product_id = p.product_id
		LEFT JOIN participants pa ON pa.tenant_id = p.tenant_id
		LEFT JOIN tenants t ON t.tenant_id = p.tenant_id
		LEFT JOIN moderation_verdicts mv ON mv.tenant_id = p.tenant_id AND mv.product_id = p.product_id`

	selectOffer = offerSelect + `
		WHERE p.tenant_id = $1 AND p.product_id = $2`

	selectTenantOffers = offerSelect + `
		WHERE p.tenant_id = $1 AND p.product_id > $2
		ORDER BY p.product_id
		LIMIT $3`

	selectParticipantOffers = offerSelect + `
		WHERE pa.tenant_id IS NOT NULL AND (p.tenant_id, p.product_id) > ($1, $2)
		ORDER BY p.tenant_id, p.product_id
		LIMIT $3`

	selectChangedOffers = offerSelect + `
		WHERE pa.tenant_id IS NOT NULL
			AND (p.tenant_id, p.product_id) > ($1, $2)
			AND (p.updated_at >= $3 OR o.updated_at >= $3 OR pa.updated_at >= $3
				OR t.updated_at >= $3 OR mv.updated_at >= $3)
		ORDER BY p.tenant_id, p.product_id
		LIMIT $4`
)

// Offers — чтение проекции для индекса.
type Offers struct {
	q querier
}

// offerRow — строка предложения: товар и всё, что к нему присоединено.
type offerRow struct {
	verdict     []byte
	product     productRow
	signals     signalsRow
	participant participantRow
	tenant      tenantRow
}

// participantRow — участник из LEFT JOIN: у товара без участника все поля пусты.
type participantRow struct {
	trust                        *string
	approvedOffers               *int
	tenantID, slug, name, status *string
	enabled                      *bool
	freeDeliveryFrom             *int64
	deliverySettings             []byte
	shopEnabled                  *bool
	subsidy                      []byte
	syncedAt                     *time.Time
	version                      *int64
	createdAt, updatedAt         *time.Time
}

// tenantRow — запись реестра из LEFT JOIN: у магазина вне реестра все поля пусты.
type tenantRow struct {
	tenantID, slug, status, cell *string
	plan, hosts                  []byte
	eventAt                      *time.Time
	version                      *int64
	updatedAt                    *time.Time
}

// Offer — снимок одного предложения.
func (r *Offers) Offer(ctx context.Context, tenantID ids.TenantID, productID ids.ProductID) (ports.OfferState, error) {
	states, err := r.query(ctx, selectOffer, string(tenantID), string(productID))
	if err != nil {
		return ports.OfferState{}, err
	}
	if len(states) == 0 {
		return ports.OfferState{}, catalog.ErrProductNotFound
	}

	return states[0], nil
}

// TenantOffers — предложения магазина по возрастанию товара.
func (r *Offers) TenantOffers(
	ctx context.Context,
	tenantID ids.TenantID,
	after ids.ProductID,
	limit int,
) ([]ports.OfferState, error) {
	return r.query(ctx, selectTenantOffers, string(tenantID), string(after), limit)
}

// ParticipantOffers — предложения всех участников по возрастанию ключа.
func (r *Offers) ParticipantOffers(ctx context.Context, after ports.OfferKey, limit int) ([]ports.OfferState, error) {
	return r.query(ctx, selectParticipantOffers, string(after.TenantID), string(after.ProductID), limit)
}

// ChangedSince — предложения участников, у которых что-то менялось не раньше since.
func (r *Offers) ChangedSince(
	ctx context.Context,
	since time.Time,
	after ports.OfferKey,
	limit int,
) ([]ports.OfferState, error) {
	return r.query(ctx, selectChangedOffers, string(after.TenantID), string(after.ProductID), since, limit)
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

func (r *offerRow) targets() []any {
	var targets []any

	targets = append(targets, r.product.targets()...)
	targets = append(targets, r.signals.targets()...)
	targets = append(targets, r.participant.targets()...)

	targets = append(targets, r.tenant.targets()...)
	return append(targets, &r.verdict)
}

func (r *offerRow) state() (ports.OfferState, error) {
	product, err := r.product.product()
	if err != nil {
		return ports.OfferState{}, err
	}

	member, err := r.participant.participant()
	if err != nil {
		return ports.OfferState{}, err
	}

	state := ports.OfferState{
		Product:     product,
		Signals:     r.signals.signals(),
		Participant: member,
		Tenant:      r.tenant.tenant(),
	}

	if len(r.verdict) > 0 {
		var v moderation.VerdictState
		if err := json.Unmarshal(r.verdict, &v); err != nil {
			return ports.OfferState{}, err
		}
		state.Verdict = moderation.RestoreVerdict(v)
	}
	return state, nil
}

func (r *participantRow) targets() []any {
	return []any{
		&r.tenantID,
		&r.slug,
		&r.name,
		&r.status,
		&r.enabled,
		&r.shopEnabled,
		&r.subsidy,
		&r.syncedAt,
		&r.version,
		&r.createdAt,
		&r.updatedAt,
		&r.freeDeliveryFrom,
		&r.deliverySettings,
		&r.trust,
		&r.approvedOffers,
	}
}

func (r *participantRow) participant() (*participant.Participant, error) {
	// у товара может не быть участника — это не ошибка
	if r.tenantID == nil {
		return nil, nil
	}

	subsidy, err := decodeSubsidy(r.subsidy)
	if err != nil {
		return nil, fmt.Errorf("субсидия участника %s: %w", *r.tenantID, err)
	}

	state := participant.State{
		Trust:          *r.trust,
		ApprovedOffers: *r.approvedOffers,
		TenantID:       ids.TenantID(*r.tenantID),
		Slug:           *r.slug,
		Name:           *r.name,
		Status:         participant.Status(*r.status),
		Enabled:        *r.enabled,
		ShopEnabled:    *r.shopEnabled,
		Subsidy:        subsidy,
		Version:        *r.version,
		CreatedAt:      *r.createdAt,
		UpdatedAt:      *r.updatedAt,
	}

	state.FreeDeliveryFrom = money.FromKopecks(*r.freeDeliveryFrom)
	if err := json.Unmarshal(r.deliverySettings, &state.Delivery); err != nil {
		return nil, err
	}
	if r.syncedAt != nil {
		state.SettingsSyncedAt = *r.syncedAt
	}

	return participant.Restore(state), nil
}

func (r *tenantRow) targets() []any {
	return []any{
		&r.tenantID,
		&r.slug,
		&r.status,
		&r.cell,
		&r.plan,
		&r.hosts,
		&r.eventAt,
		&r.version,
		&r.updatedAt,
	}
}

func (r *tenantRow) tenant() *participant.Tenant {
	if r.tenantID == nil {
		return nil
	}

	state := participant.TenantState{
		TenantID:  ids.TenantID(*r.tenantID),
		Slug:      *r.slug,
		Status:    participant.TenantStatus(*r.status),
		Cell:      *r.cell,
		Plan:      r.plan,
		Hosts:     r.hosts,
		EventAt:   *r.eventAt,
		Version:   *r.version,
		UpdatedAt: *r.updatedAt,
	}

	return participant.RestoreTenant(state)
}
