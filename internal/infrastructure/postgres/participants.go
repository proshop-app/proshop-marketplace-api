package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	participantColumns = `
		tenant_id, slug, name, stage, enabled, awaiting_review, subsidy,
		settings_synced_at, version, created_at, updated_at, free_delivery_from,
		delivery_settings, trust, approved_offers, last_decision`

	selectParticipant = `
		SELECT ` + participantColumns + `
		FROM participants
		WHERE tenant_id = $1`

	countParticipants = `SELECT count(*) FROM participants WHERE ($1 = '' OR trust = $1)
		AND ($2 = '' OR stage = $2) AND (NOT $3 OR (stage = 'candidate' AND awaiting_review AND delivery_valid))`

	listParticipants = `
		SELECT ` + participantColumns + `
		FROM participants WHERE ($3 = '' OR trust = $3)
		AND ($4 = '' OR stage = $4) AND (NOT $5 OR (stage = 'candidate' AND awaiting_review AND delivery_valid))
		ORDER BY created_at, tenant_id
		LIMIT $1 OFFSET $2`

	insertParticipant = `
		INSERT INTO participants (
			tenant_id, slug, name, stage, enabled, awaiting_review, subsidy,
			settings_synced_at, version, created_at, updated_at, free_delivery_from,
			delivery_settings, trust, approved_offers, delivery_valid, last_decision
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT DO NOTHING`

	// selectInsertConflict говорит, чья строка помешала вставке: тот же магазин или
	// чужой с тем же слагом.
	selectInsertConflict = `
		SELECT tenant_id = $1
		FROM participants
		WHERE tenant_id = $1 OR slug = $2
		ORDER BY tenant_id = $1 DESC
		LIMIT 1`

	updateParticipant = `
		UPDATE participants
		SET slug = $4,
			name = $5,
			stage = $6,
			enabled = $7,
			awaiting_review = $8,
			subsidy = $9,
			settings_synced_at = $10,
			version = $11,
			updated_at = $12,
			free_delivery_from = $13,
			delivery_settings = $14,
			trust = $15,
			approved_offers = $16,
			delivery_valid = $17,
			last_decision = $18
		WHERE tenant_id = $1 AND stage = $2 AND version = $3`
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
	if err := r.q.QueryRow(ctx, countParticipants, page.Trust, page.Stage, page.Queue).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("число участников: %w", err)
	}

	rows, err := r.q.Query(ctx, listParticipants, page.Limit, page.Offset, page.Trust, page.Stage, page.Queue)
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

	tag, err := r.q.Exec(
		ctx,
		insertParticipant,
		string(s.TenantID),
		s.Slug,
		s.Name,
		string(s.Stage),
		s.Enabled,
		s.AwaitingReview,
		subsidy,
		nullTime(s.SettingsSyncedAt),
		s.Version,
		s.CreatedAt,
		s.UpdatedAt,
		s.FreeDeliveryFrom.Kopecks(),
		encodeDelivery(s.Delivery),
		s.Trust,
		s.ApprovedOffers,
		p.DeliveryValid(),
		encodeLastDecision(s.LastDecision),
	)

	switch {
	case err == nil && tag.RowsAffected() == 0:
		return r.insertConflict(ctx, s)
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
		string(origin.Stage),
		origin.Version,
		s.Slug,
		s.Name,
		string(s.Stage),
		s.Enabled,
		s.AwaitingReview,
		subsidy,
		nullTime(s.SettingsSyncedAt),
		s.Version,
		s.UpdatedAt,
		s.FreeDeliveryFrom.Kopecks(),
		encodeDelivery(s.Delivery),
		s.Trust,
		s.ApprovedOffers,
		p.DeliveryValid(),
		encodeLastDecision(s.LastDecision),
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
		stage    string
		subsidy  []byte
		synced   *time.Time
		free     int64
		delivery []byte
		decision []byte
	)

	err := row.Scan(
		&tenantID,
		&state.Slug,
		&state.Name,
		&stage,
		&state.Enabled,
		&state.AwaitingReview,
		&subsidy,
		&synced,
		&state.Version,
		&state.CreatedAt,
		&state.UpdatedAt,
		&free,
		&delivery,
		&state.Trust,
		&state.ApprovedOffers,
		&decision,
	)
	if err != nil {
		return nil, err
	}

	if len(decision) > 0 {
		if err := json.Unmarshal(decision, &state.LastDecision); err != nil {
			return nil, err
		}
	}
	state.FreeDeliveryFrom = money.FromKopecks(free)
	if err := json.Unmarshal(delivery, &state.Delivery); err != nil {
		return nil, err
	}
	state.TenantID = ids.TenantID(tenantID)
	state.Stage = participant.Stage(stage)

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

func encodeLastDecision(d *participant.LastDecision) any {
	if d == nil {
		return nil
	}
	raw, _ := json.Marshal(d)
	return string(raw)
}

// Count считает участников и очередь без чтения снимков.
func (r *Participants) Count(ctx context.Context, page participant.Page) (int, error) {
	var total int
	err := r.q.QueryRow(ctx, countParticipants, page.Trust, page.Stage, page.Queue).Scan(&total)
	return total, err
}

// insertConflict различает гонку за тот же магазин и занятый слаг. ON CONFLICT с целью
// tenant_id гасит только конфликт по первичному ключу: при одновременной вставке того же
// магазина проигравшая транзакция упиралась в индекс слага и получала «слаг занят».
// Без цели вставка ничего не делает при любом конфликте уникальности, а причину читает этот
// запрос: в READ COMMITTED он видит строку, закоммиченную соседней транзакцией.
func (r *Participants) insertConflict(ctx context.Context, s participant.State) error {
	var sameTenant bool

	err := r.q.QueryRow(ctx, selectInsertConflict, string(s.TenantID), s.Slug).Scan(&sameTenant)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("новый участник %s: вставка не выполнена, конфликтующей строки нет", s.TenantID)
	case err != nil:
		return fmt.Errorf("новый участник %s: %w", s.TenantID, err)
	case sameTenant:
		return participant.ErrAlreadyExists
	default:
		return participant.ErrSlugTaken
	}
}
