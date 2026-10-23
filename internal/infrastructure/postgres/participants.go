package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const participantColumns = `tenant_id, slug, name, status, enabled, coalesce(payment_account_id, ''), shop_enabled, subsidy,
	settings_synced_at, version, created_at, updated_at`

// Participants — участники в PostgreSQL.
type Participants struct {
	q querier
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanParticipant(row rowScanner) (*participant.Participant, error) {
	var (
		state    participant.State
		tenantID string
		status   string
		subsidy  []byte
		synced   *time.Time
	)

	if err := row.Scan(&tenantID, &state.Slug, &state.Name, &status, &state.Enabled, &state.PaymentAccountID,
		&state.ShopEnabled, &subsidy, &synced, &state.Version, &state.CreatedAt, &state.UpdatedAt); err != nil {
		return nil, err
	}

	state.TenantID = ids.TenantID(tenantID)
	state.Status = participant.Status(status)

	if synced != nil {
		state.SettingsSyncedAt = *synced
	}

	var err error
	if state.Subsidy, err = decodeSubsidy(subsidy); err != nil {
		return nil, fmt.Errorf("субсидия участника %s: %w", tenantID, err)
	}

	return participant.Restore(state), nil
}

// subsidyJSON — субсидия в колонке jsonb: value — копейки или процент, maxAmount — копейки.
type subsidyJSON struct {
	Enabled   bool   `json:"enabled"`
	Type      string `json:"type,omitempty"`
	Value     int64  `json:"value"`
	MaxAmount int64  `json:"maxAmount"`
}

func encodeSubsidy(s participant.Subsidy) (string, error) {
	encoded, err := json.Marshal(subsidyJSON{Enabled: s.Enabled, Type: string(s.Type), Value: s.Value, MaxAmount: s.MaxAmount.Kopecks()})

	return string(encoded), err
}

func decodeSubsidy(raw []byte) (participant.Subsidy, error) {
	if len(raw) == 0 {
		return participant.Subsidy{}, nil
	}

	var doc subsidyJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return participant.Subsidy{}, err
	}

	return participant.Subsidy{
		Enabled: doc.Enabled, Type: participant.SubsidyType(doc.Type), Value: doc.Value, MaxAmount: money.FromKopecks(doc.MaxAmount),
	}, nil
}

// Get — участник.
func (r *Participants) Get(ctx context.Context, tenantID ids.TenantID) (*participant.Participant, error) {
	p, err := scanParticipant(r.q.QueryRow(ctx, `SELECT `+participantColumns+` FROM participants WHERE tenant_id = $1`, string(tenantID)))
	if notFound(err) {
		return nil, participant.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("участник %s: %w", tenantID, err)
	}

	return p, nil
}

// List — участники по времени создания.
func (r *Participants) List(ctx context.Context, page participant.Page) ([]*participant.Participant, int, error) {
	var total int
	if err := r.q.QueryRow(ctx, `SELECT count(*) FROM participants`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("число участников: %w", err)
	}

	rows, err := r.q.Query(ctx, `SELECT `+participantColumns+` FROM participants
		ORDER BY created_at, tenant_id LIMIT $1 OFFSET $2`, page.Limit, page.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("список участников: %w", err)
	}
	defer rows.Close()

	items := make([]*participant.Participant, 0, page.Limit)
	for rows.Next() {
		p, err := scanParticipant(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("список участников: %w", err)
		}

		items = append(items, p)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("список участников: %w", err)
	}

	return items, total, nil
}

// Add сохраняет нового участника.
func (r *Participants) Add(ctx context.Context, p *participant.Participant) error {
	s := p.State()

	subsidy, err := encodeSubsidy(s.Subsidy)
	if err != nil {
		return fmt.Errorf("субсидия участника %s: %w", s.TenantID, err)
	}

	_, err = r.q.Exec(ctx, `INSERT INTO participants
		(tenant_id, slug, name, status, enabled, payment_account_id, shop_enabled, subsidy, settings_synced_at, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), $7, $8, $9, $10, $11, $12)`,
		string(s.TenantID), s.Slug, s.Name, string(s.Status), s.Enabled, s.PaymentAccountID, s.ShopEnabled, subsidy,
		nullTime(s.SettingsSyncedAt), s.Version, s.CreatedAt, s.UpdatedAt)

	switch {
	case isUnique(err, "participants_pkey"):
		return participant.ErrAlreadyExists
	case isUnique(err, "participants_slug_key"):
		return participant.ErrSlugTaken
	case isForeignKey(err):
		return participant.ErrTenantUnknown
	case err != nil:
		return fmt.Errorf("новый участник %s: %w", s.TenantID, err)
	}

	p.MarkSaved()

	return nil
}

// Update сохраняет изменения условно: статус и версия в базе должны совпасть
// с прочитанными, иначе ErrConflict.
func (r *Participants) Update(ctx context.Context, p *participant.Participant) error {
	s := p.State()
	origin := p.Origin()

	subsidy, err := encodeSubsidy(s.Subsidy)
	if err != nil {
		return fmt.Errorf("субсидия участника %s: %w", s.TenantID, err)
	}

	tag, err := r.q.Exec(ctx, `UPDATE participants
		SET slug = $3, name = $4, status = $5, enabled = $6, payment_account_id = nullif($7, ''), version = $8, updated_at = $9,
			shop_enabled = $11, subsidy = $12, settings_synced_at = $13
		WHERE tenant_id = $1 AND status = $2 AND version = $10`,
		string(s.TenantID), string(origin.Status), s.Slug, s.Name, string(s.Status), s.Enabled, s.PaymentAccountID,
		s.Version, s.UpdatedAt, origin.Version, s.ShopEnabled, subsidy, nullTime(s.SettingsSyncedAt))

	switch {
	case isUnique(err, "participants_slug_key"):
		return participant.ErrSlugTaken
	case err != nil:
		return fmt.Errorf("участник %s: %w", s.TenantID, err)
	case tag.RowsAffected() == 0:
		return participant.ErrConflict
	}

	p.MarkSaved()

	return nil
}

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

	err := r.q.QueryRow(ctx, `SELECT tenant_id, slug, status, cell, plan, hosts, event_at, version, updated_at
		FROM tenants WHERE tenant_id = $1`, string(tenantID)).
		Scan(&id, &state.Slug, &status, &state.Cell, &plan, &hosts, &state.EventAt, &state.Version, &state.UpdatedAt)
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
	args := []any{string(s.TenantID), s.Slug, string(s.Status), s.Cell, jsonArg(s.Plan), jsonArg(s.Hosts), s.EventAt, s.Version, s.UpdatedAt}

	if tenant.OriginVersion() == 0 {
		_, err := r.q.Exec(ctx, `INSERT INTO tenants (tenant_id, slug, status, cell, plan, hosts, event_at, version, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, args...)
		if isUnique(err, "tenants_pkey") {
			return participant.ErrTenantConflict
		}
		if err != nil {
			return fmt.Errorf("новая запись реестра %s: %w", s.TenantID, err)
		}

		tenant.MarkSaved()

		return nil
	}

	tag, err := r.q.Exec(ctx, `UPDATE tenants
		SET slug = $2, status = $3, cell = $4, plan = $5, hosts = $6, event_at = $7, version = $8, updated_at = $9
		WHERE tenant_id = $1 AND version = $10`, append(args, tenant.OriginVersion())...)
	if err != nil {
		return fmt.Errorf("запись реестра %s: %w", s.TenantID, err)
	}
	if tag.RowsAffected() == 0 {
		return participant.ErrTenantConflict
	}

	tenant.MarkSaved()

	return nil
}

// jsonArg — сырой JSON как параметр jsonb; пусто — NULL.
func jsonArg(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}

	return string(raw)
}

// nullTime — время как параметр; нулевое — NULL.
func nullTime(at time.Time) any {
	if at.IsZero() {
		return nil
	}

	return at
}
