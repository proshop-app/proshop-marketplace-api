package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	participantColumns = `
		tenant_id, slug, name, stage, enabled, awaiting_review,
		settings_synced_at, version, created_at, updated_at,
		delivery_settings, trust, approved_offers, last_decision, store_card`

	selectParticipant = `
		SELECT ` + participantColumns + `
		FROM participants
		WHERE tenant_id = $1`

	countParticipants = `SELECT count(*) FROM participants WHERE ($1 = '' OR trust = $1)
		AND ($2 = '' OR stage = $2) AND (NOT $3 OR (stage = 'candidate' AND awaiting_review
		AND delivery_valid AND card_ready))`

	listParticipants = `
		SELECT ` + participantColumns + `
		FROM participants WHERE ($3 = '' OR trust = $3)
		AND ($4 = '' OR stage = $4) AND (NOT $5 OR (stage = 'candidate' AND awaiting_review
		AND delivery_valid AND card_ready))
		ORDER BY created_at, tenant_id
		LIMIT $1 OFFSET $2`

	listParticipantTenantIDs = `SELECT tenant_id FROM participants WHERE tenant_id > $1 ORDER BY tenant_id LIMIT $2`

	insertParticipant = `
		INSERT INTO participants (
			tenant_id, slug, name, stage, enabled, awaiting_review,
			settings_synced_at, version, created_at, updated_at,
			delivery_settings, trust, approved_offers, delivery_valid, last_decision, store_card, card_ready
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
			settings_synced_at = $9,
			version = $10,
			updated_at = $11,
			delivery_settings = $12,
			trust = $13,
			approved_offers = $14,
			delivery_valid = $15,
			last_decision = $16,
			store_card = $17,
			card_ready = $18
		WHERE tenant_id = $1 AND stage = $2 AND version = $3`
)

// Participants — участники в PostgreSQL.
type Participants struct {
	db *gorm.DB
}

// rowScanner объединяет чтение одной строки и набора строк database/sql.
type rowScanner interface {
	Scan(dest ...any) error
}

// Get — участник.
func (r *Participants) Get(ctx context.Context, tenantID ids.TenantID) (*participant.Participant, error) {
	p, err := scanParticipant(rawSQL(ctx, r.db, selectParticipant, string(tenantID)).Row())
	if notFound(err) {
		return nil, participant.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("участник %s: %w", tenantID, err)
	}

	return p, nil
}

// TenantIDsAfter читает только ключи следующей страницы через первичный индекс.
func (r *Participants) TenantIDsAfter(ctx context.Context, after ids.TenantID, limit int) ([]ids.TenantID, error) {
	rows, err := rawSQL(ctx, r.db, listParticipantTenantIDs, string(after), limit).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	keys := make([]ids.TenantID, 0, limit)
	for rows.Next() {
		var key ids.TenantID
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// List — участники по времени создания.
func (r *Participants) List(ctx context.Context, page participant.Page) ([]*participant.Participant, int, error) {
	var total int
	args := []any{page.Trust, page.Stage, page.Queue}
	if err := rawSQL(ctx, r.db, countParticipants, args...).Row().Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("число участников: %w", err)
	}

	rows, err := rawSQL(
		ctx,
		r.db,
		listParticipants,
		page.Limit,
		page.Offset,
		page.Trust,
		page.Stage,
		page.Queue,
	).Rows()
	if err != nil {
		return nil, 0, fmt.Errorf("список участников: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

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

	card, err := encodeStoreCard(s.StoreCard)
	if err != nil {
		return fmt.Errorf("карточка участника %s: %w", s.TenantID, err)
	}

	affected, err := execSQL(
		ctx,
		r.db,
		insertParticipant,
		string(s.TenantID),
		s.Slug,
		s.Name,
		string(s.Stage),
		s.Enabled,
		s.AwaitingReview,
		nullTime(s.SettingsSyncedAt),
		s.Version,
		s.CreatedAt,
		s.UpdatedAt,
		encodeDelivery(s.Delivery),
		s.Trust,
		s.ApprovedOffers,
		p.DeliveryValid(),
		encodeLastDecision(s.LastDecision),
		card,
		p.CardReady(),
	)

	switch {
	case err == nil && affected == 0:
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

	card, err := encodeStoreCard(s.StoreCard)
	if err != nil {
		return fmt.Errorf("карточка участника %s: %w", s.TenantID, err)
	}

	affected, err := execSQL(
		ctx,
		r.db,
		updateParticipant,
		string(s.TenantID),
		string(origin.Stage),
		origin.Version,
		s.Slug,
		s.Name,
		string(s.Stage),
		s.Enabled,
		s.AwaitingReview,
		nullTime(s.SettingsSyncedAt),
		s.Version,
		s.UpdatedAt,
		encodeDelivery(s.Delivery),
		s.Trust,
		s.ApprovedOffers,
		p.DeliveryValid(),
		encodeLastDecision(s.LastDecision),
		card,
		p.CardReady(),
	)

	switch {
	case isUnique(err, "participants_slug_key"):
		return participant.ErrSlugTaken
	case err != nil:
		return fmt.Errorf("участник %s: %w", s.TenantID, err)
	case affected == 0:
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
		synced   *time.Time
		delivery []byte
		decision []byte
		card     []byte
	)

	err := row.Scan(
		&tenantID,
		&state.Slug,
		&state.Name,
		&stage,
		&state.Enabled,
		&state.AwaitingReview,
		&synced,
		&state.Version,
		&state.CreatedAt,
		&state.UpdatedAt,
		&delivery,
		&state.Trust,
		&state.ApprovedOffers,
		&decision,
		&card,
	)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(card, &state.StoreCard); err != nil {
		return nil, fmt.Errorf("карточка участника %s: %w", state.TenantID, err)
	}
	if len(decision) > 0 {
		if err := json.Unmarshal(decision, &state.LastDecision); err != nil {
			return nil, err
		}
	}
	if err := json.Unmarshal(delivery, &state.Delivery); err != nil {
		return nil, err
	}
	state.TenantID = ids.TenantID(tenantID)
	state.Stage = participant.Stage(stage)

	if synced != nil {
		state.SettingsSyncedAt = *synced
	}

	return participant.Restore(state), nil
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
	args := []any{page.Trust, page.Stage, page.Queue}
	err := rawSQL(ctx, r.db, countParticipants, args...).Row().Scan(&total)
	return total, err
}

// insertConflict различает гонку за тот же магазин и занятый слаг. ON CONFLICT с целью
// tenant_id гасит только конфликт по первичному ключу: при одновременной вставке того же
// магазина проигравшая транзакция упиралась в индекс слага и получала «слаг занят».
// Без цели вставка ничего не делает при любом конфликте уникальности, а причину читает этот
// запрос: в READ COMMITTED он видит строку, закоммиченную соседней транзакцией.
func (r *Participants) insertConflict(ctx context.Context, s participant.State) error {
	var sameTenant bool

	err := rawSQL(ctx, r.db, selectInsertConflict, string(s.TenantID), s.Slug).Row().Scan(&sameTenant)

	switch {
	case notFound(err):
		return fmt.Errorf("новый участник %s: вставка не выполнена, конфликтующей строки нет", s.TenantID)
	case err != nil:
		return fmt.Errorf("новый участник %s: %w", s.TenantID, err)
	case sameTenant:
		return participant.ErrAlreadyExists
	default:
		return participant.ErrSlugTaken
	}
}

func encodeStoreCard(c participant.StoreCard) (string, error) {
	raw, err := json.Marshal(c)
	return string(raw), err
}
