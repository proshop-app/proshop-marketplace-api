package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	insertModerationDecision = `INSERT INTO moderation_decisions
 (id, case_id, operator_id, operator_name, action, reason, note, revision, at)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	listModerationDecisions = `SELECT d.id, d.case_id, d.operator_id, d.operator_name,
 d.action, d.reason, d.note, d.revision, d.at
 FROM moderation_decisions d JOIN moderation_cases c ON c.id = d.case_id
 WHERE c.tenant_id = $1 AND c.product_id = $2 ORDER BY d.at DESC, d.id DESC`
)

// ModerationDecisions не предоставляет операций изменения истории.
type ModerationDecisions struct{ q querier }

// Add добавляет решение без изменения прежних записей.
func (r *ModerationDecisions) Add(ctx context.Context, d moderation.Decision) error {
	_, err := r.q.Exec(
		ctx,
		insertModerationDecision,
		d.ID,
		d.CaseID,
		d.Operator.ID,
		d.Operator.Name,
		d.Action,
		d.Reason,
		d.Note,
		d.Revision,
		d.At,
	)
	return err
}

// List ограничивает объём чтения для операторского сценария.
func (r *ModerationDecisions) List(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) ([]moderation.Decision, error) {
	rows, err := r.q.Query(ctx, listModerationDecisions, tenant, product)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []moderation.Decision{}
	for rows.Next() {
		var d moderation.Decision
		err := rows.Scan(
			&d.ID,
			&d.CaseID,
			&d.Operator.ID,
			&d.Operator.Name,
			&d.Action,
			&d.Reason,
			&d.Note,
			&d.Revision,
			&d.At,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}
