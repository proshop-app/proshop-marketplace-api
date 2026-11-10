package postgres

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	historyModerationVerdicts = `SELECT tenant_id, product_id, revision, rules_revision,
 taxonomy_version, status, reasons, decided_by, manual_reject, version, at,
 operator_id, operator_name, decision FROM moderation_verdict_log
 WHERE tenant_id = $1 AND product_id = $2 ORDER BY at DESC, id DESC LIMIT 50`
	verdictColumns = `tenant_id, product_id, revision, rules_revision,
 taxonomy_version, status, reasons, decided_by,
 manual_reject, version, updated_at, operator_id, operator_name, decision`
	selectModerationVerdict = `SELECT ` + verdictColumns + ` FROM moderation_verdicts
 WHERE tenant_id = $1 AND product_id = $2`
	insertModerationVerdict = `INSERT INTO moderation_verdicts (
 tenant_id, product_id, revision, rules_revision,
 taxonomy_version, status, reasons, decided_by,
 manual_reject, version, updated_at, operator_id, operator_name, decision
 ) VALUES (
 $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
 ) ON CONFLICT DO NOTHING`
	updateModerationVerdict = `UPDATE moderation_verdicts SET
 revision = $3,
 rules_revision = $4,
 taxonomy_version = $5,
 status = $6,
 reasons = $7,
 decided_by = $8,
 manual_reject = $9,
 version = $10,
 updated_at = $11,
 operator_id = $12,
 operator_name = $13,
 decision = $14
 WHERE tenant_id = $1 AND product_id = $2 AND version = $15 AND status = $16`
	countModerationVerdicts = `SELECT count(*) FROM moderation_verdicts
 WHERE ($1 = '' OR status = $1) AND ($2 = '' OR tenant_id = $2) AND ($3 = '' OR decided_by = $3)`
	listModerationVerdicts = `SELECT ` + verdictColumns + ` FROM moderation_verdicts
 WHERE ($1 = '' OR status = $1) AND ($2 = '' OR tenant_id = $2) AND ($3 = '' OR decided_by = $3) 
 ORDER BY updated_at DESC, tenant_id, product_id LIMIT $4 OFFSET $5`
)

// ModerationVerdicts сохраняет вердикты условно по исходной версии.
type ModerationVerdicts struct {
	db *gorm.DB
}

// Get читает исходное состояние для условного сохранения.
func (r *ModerationVerdicts) Get(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (*moderation.Verdict, error) {
	return scanModerationVerdict(rawSQL(ctx, r.db, selectModerationVerdict, tenant, product).Row())
}

func scanModerationVerdict(row rowScanner) (*moderation.Verdict, error) {
	var s moderation.VerdictState
	var reasons, decision []byte
	err := row.Scan(
		&s.TenantID,
		&s.ProductID,
		&s.Revision,
		&s.RulesRevision,
		&s.TaxonomyVersion,
		&s.Status,
		&reasons,
		&s.DecidedBy,
		&s.ManualReject,
		&s.Version,
		&s.UpdatedAt,
		&s.Operator.ID,
		&s.Operator.Name,
		&decision,
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
	if len(decision) > 0 {
		if err := json.Unmarshal(decision, &s.Decision); err != nil {
			return nil, err
		}
	}
	return moderation.RestoreVerdict(s), nil
}

// Save не допускает тихой перезаписи конкурентного изменения.
func (r *ModerationVerdicts) Save(ctx context.Context, value *moderation.Verdict) error {
	s := value.State()
	reasons, err := json.Marshal(s.Reasons)
	if err != nil {
		return err
	}
	decision, err := json.Marshal(s.Decision)
	if err != nil {
		return err
	}
	args := []any{
		s.TenantID,
		s.ProductID,
		s.Revision,
		s.RulesRevision,
		s.TaxonomyVersion,
		s.Status,
		reasons,
		s.DecidedBy,
		s.ManualReject,
		s.Version,
		s.UpdatedAt,
		s.Operator.ID,
		s.Operator.Name,
		decision,
	}
	query := insertModerationVerdict
	if value.Origin().Version > 0 {
		query = updateModerationVerdict
		args = append(args, value.Origin().Version, value.Origin().Status)
	}
	affected, err := execSQL(ctx, r.db, query, args...)
	if isUnique(err, "moderation_case_open") {
		return moderation.ErrConflict
	}
	if err != nil {
		return err
	}
	if affected == 0 {
		return moderation.ErrConflict
	}
	value.MarkSaved()
	return nil
}

// List ограничивает объём чтения для операторского сценария.
func (r *ModerationVerdicts) List(ctx context.Context, f moderation.Filter) ([]moderation.VerdictState, int, error) {
	var total int
	row := rawSQL(ctx, r.db, countModerationVerdicts, f.Status, f.TenantID, f.DecidedBy).Row()
	if err := row.Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := rawSQL(ctx, r.db, listModerationVerdicts, f.Status, f.TenantID, f.DecidedBy, f.Limit, f.Offset).Rows()
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_ = rows.Close()
	}()
	items := []moderation.VerdictState{}
	for rows.Next() {
		value, err := scanModerationVerdict(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, value.State())
	}
	return items, total, rows.Err()
}

// History читает сохранённые оценки, включая автоматические переоценки.
func (r *ModerationVerdicts) History(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) ([]moderation.VerdictState, error) {
	rows, err := rawSQL(ctx, r.db, historyModerationVerdicts, tenant, product).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := []moderation.VerdictState{}
	for rows.Next() {
		v, err := scanModerationVerdict(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, v.State())
	}
	return result, rows.Err()
}
