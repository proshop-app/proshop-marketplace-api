package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	moderationOpenStats = `SELECT tenant_id, count(*), min(opened_at)
 FROM moderation_cases WHERE status = 'open' GROUP BY tenant_id`
	caseColumns = `id, tenant_id, product_id, revision,
 source, status, reasons, note,
 opened_at, decided_at, version`
	selectModerationCase = `SELECT ` + caseColumns + ` FROM moderation_cases WHERE id = $1`
	insertModerationCase = `INSERT INTO moderation_cases (
 id, tenant_id, product_id, revision,
 source, status, reasons, note,
 opened_at, decided_at, version
 ) VALUES (
 $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
 ) ON CONFLICT DO NOTHING`
	updateModerationCase = `UPDATE moderation_cases SET
 tenant_id = $2,
 product_id = $3,
 revision = $4,
 source = $5,
 status = $6,
 reasons = $7,
 note = $8,
 opened_at = $9,
 decided_at = $10,
 version = $11
 WHERE id = $1 AND version = $12 AND status = $13`
	countModerationCases = `SELECT count(*) FROM moderation_cases
 WHERE ($1 = '' OR status = $1) AND ($2 = '' OR tenant_id = $2) AND ($3 = '' OR source = $3)`
	listModerationCases = `SELECT ` + caseColumns + ` FROM moderation_cases

 WHERE ($1 = '' OR status = $1) AND ($2 = '' OR tenant_id = $2) AND ($3 = '' OR source = $3)
 ORDER BY opened_at DESC, id DESC LIMIT $4 OFFSET $5`
	openModerationCase = `SELECT ` + caseColumns + ` FROM moderation_cases
 WHERE tenant_id = $1 AND product_id = $2 AND status = 'open'`
)

// ModerationCases сохраняет дела условно по исходной версии и статусу.
type ModerationCases struct{ q querier }

// Get читает исходное состояние для условного сохранения.
func (r *ModerationCases) Get(ctx context.Context, id ids.UUID) (*moderation.Case, error) {
	return scanModerationCase(r.q.QueryRow(ctx, selectModerationCase, id))
}

func scanModerationCase(row rowScanner) (*moderation.Case, error) {
	var s moderation.CaseState
	var reasons []byte
	err := row.Scan(
		&s.ID,
		&s.TenantID,
		&s.ProductID,
		&s.Revision,
		&s.Source,
		&s.Status,
		&reasons,
		&s.Note,
		&s.OpenedAt,
		&s.DecidedAt,
		&s.Version,
	)
	if notFound(err) {
		return nil, moderation.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(reasons, &s.Reasons); err != nil {
		return nil, err
	}
	return moderation.RestoreCase(s), nil
}

// Save не допускает тихой перезаписи конкурентного изменения.
func (r *ModerationCases) Save(ctx context.Context, value *moderation.Case) error {
	s := value.State()
	reasons, err := json.Marshal(s.Reasons)
	if err != nil {
		return err
	}
	args := []any{
		s.ID,
		s.TenantID,
		s.ProductID,
		s.Revision,
		s.Source,
		s.Status,
		reasons,
		s.Note,
		s.OpenedAt,
		s.DecidedAt,
		s.Version,
	}
	query := insertModerationCase
	if value.Origin().Version > 0 {
		query = updateModerationCase
		args = append(args, value.Origin().Version, value.Origin().Status)
	}
	tag, err := r.q.Exec(ctx, query, args...)
	if isUnique(err, "moderation_case_open") {
		return moderation.ErrCaseConflict
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		current, err := r.Get(ctx, s.ID)
		if errors.Is(err, moderation.ErrNotFound) {
			current, err = r.Open(ctx, s.TenantID, s.ProductID)
		}
		if err != nil {
			if errors.Is(err, moderation.ErrNotFound) {
				return moderation.ErrCaseConflict
			}
			return err
		}
		return &moderation.CaseConflict{Current: current.State()}
	}
	value.MarkSaved()
	return nil
}

// List ограничивает объём чтения для операторского сценария.
func (r *ModerationCases) List(ctx context.Context, f moderation.Filter) ([]moderation.CaseState, int, error) {
	var total int
	if err := r.q.QueryRow(ctx, countModerationCases, f.Status, f.TenantID, f.Source).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.q.Query(ctx, listModerationCases, f.Status, f.TenantID, f.Source, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []moderation.CaseState{}
	for rows.Next() {
		value, err := scanModerationCase(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, value.State())
	}
	return items, total, rows.Err()
}

// Open находит единственное незакрытое дело предложения.
func (r *ModerationCases) Open(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (*moderation.Case, error) {
	return scanModerationCase(r.q.QueryRow(ctx, openModerationCase, tenant, product))
}

// OpenStats не загружает сами дела для подсчёта очереди.
func (r *ModerationCases) OpenStats(ctx context.Context) ([]moderation.OpenStat, error) {
	rows, err := r.q.Query(ctx, moderationOpenStats)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []moderation.OpenStat{}
	for rows.Next() {
		var stat moderation.OpenStat
		if err := rows.Scan(&stat.TenantID, &stat.Count, &stat.Oldest); err != nil {
			return nil, err
		}
		result = append(result, stat)
	}
	return result, rows.Err()
}
