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

const (
	participantColumns = `
		tenant_id, slug, name, status, enabled, shop_enabled, subsidy,
		settings_synced_at, version, created_at, updated_at, free_delivery_from,
		delivery_settings, trust, approved_offers`

	selectParticipant = `
		SELECT ` + participantColumns + `
		FROM participants
		WHERE tenant_id = $1`

	countParticipants = `SELECT count(*) FROM participants WHERE ($1 = '' OR trust = $1)`

	listParticipants = `
		SELECT ` + participantColumns + `
		FROM participants WHERE ($3 = '' OR trust = $3)
		ORDER BY created_at, tenant_id
		LIMIT $1 OFFSET $2`

	insertParticipant = `
		INSERT INTO participants (
			tenant_id, slug, name, status, enabled, shop_enabled, subsidy,
			settings_synced_at, version, created_at, updated_at, free_delivery_from,
		delivery_settings, trust, approved_offers
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`

	updateParticipant = `
		UPDATE participants
		SET slug = $4,
			name = $5,
			status = $6,
			enabled = $7,
			shop_enabled = $8,
			subsidy = $9,
			settings_synced_at = $10,
			version = $11,
			updated_at = $12,
			free_delivery_from = $13,
			delivery_settings = $14,
			trust = $15,
			approved_offers = $16
		WHERE tenant_id = $1 AND status = $2 AND version = $3`
)

// Participants — участники в PostgreSQL.
type Participants struct {
	q querier
}

// rowScanner — общее у pgx.Row и pgx.Rows для разбора одной строки.
type rowScanner interface {
	Scan(dest ...any) error
}

// subsidyJSON — субсидия в колонке jsonb: value — копейки или процент, maxAmount — копейки.
type subsidyJSON struct {
	Enabled   bool   `json:"enabled"`
	Type      string `json:"type,omitempty"`
	Value     int64  `json:"value"`
	MaxAmount int64  `json:"maxAmount"`
}

// Get — участник.
func (r *Participants) Get(ctx context.Context, tenantID ids.TenantID) (*participant.Participant, error) {
	p, err := scanParticipant(r.q.QueryRow(ctx, selectParticipant, string(tenantID)))
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
	if err := r.q.QueryRow(ctx, countParticipants, page.Trust).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("число участников: %w", err)
	}

	rows, err := r.q.Query(ctx, listParticipants, page.Limit, page.Offset, page.Trust)
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

	_, err = r.q.Exec(
		ctx,
		insertParticipant,
		string(s.TenantID),
		s.Slug,
		s.Name,
		string(s.Status),
		s.Enabled,
		s.ShopEnabled,
		subsidy,
		nullTime(s.SettingsSyncedAt),
		s.Version,
		s.CreatedAt,
		s.UpdatedAt,
		s.FreeDeliveryFrom.Kopecks(),
		encodeDelivery(s.Delivery),
		s.Trust,
		s.ApprovedOffers,
	)

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

	tag, err := r.q.Exec(
		ctx,
		updateParticipant,
		string(s.TenantID),
		string(origin.Status),
		origin.Version,
		s.Slug,
		s.Name,
		string(s.Status),
		s.Enabled,
		s.ShopEnabled,
		subsidy,
		nullTime(s.SettingsSyncedAt),
		s.Version,
		s.UpdatedAt,
		s.FreeDeliveryFrom.Kopecks(),
		encodeDelivery(s.Delivery),
		s.Trust,
		s.ApprovedOffers,
	)

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

func scanParticipant(row rowScanner) (*participant.Participant, error) {
	var (
		state    participant.State
		tenantID string
		status   string
		subsidy  []byte
		synced   *time.Time
		free     int64
		delivery []byte
	)

	err := row.Scan(
		&tenantID,
		&state.Slug,
		&state.Name,
		&status,
		&state.Enabled,
		&state.ShopEnabled,
		&subsidy,
		&synced,
		&state.Version,
		&state.CreatedAt,
		&state.UpdatedAt,
		&free,
		&delivery,
		&state.Trust,
		&state.ApprovedOffers,
	)
	if err != nil {
		return nil, err
	}

	state.FreeDeliveryFrom = money.FromKopecks(free)
	if err := json.Unmarshal(delivery, &state.Delivery); err != nil {
		return nil, err
	}
	state.TenantID = ids.TenantID(tenantID)
	state.Status = participant.Status(status)

	if synced != nil {
		state.SettingsSyncedAt = *synced
	}

	if state.Subsidy, err = decodeSubsidy(subsidy); err != nil {
		return nil, fmt.Errorf("субсидия участника %s: %w", tenantID, err)
	}

	return participant.Restore(state), nil
}

func encodeSubsidy(s participant.Subsidy) (string, error) {
	doc := subsidyJSON{
		Enabled:   s.Enabled,
		Type:      string(s.Type),
		Value:     s.Value,
		MaxAmount: s.MaxAmount.Kopecks(),
	}

	encoded, err := json.Marshal(doc)

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

	subsidy := participant.Subsidy{
		Enabled:   doc.Enabled,
		Type:      participant.SubsidyType(doc.Type),
		Value:     doc.Value,
		MaxAmount: money.FromKopecks(doc.MaxAmount),
	}

	return subsidy, nil
}

func encodeDelivery(d participant.DeliverySettings) string {
	raw, _ := json.Marshal(d)
	return string(raw)
}
