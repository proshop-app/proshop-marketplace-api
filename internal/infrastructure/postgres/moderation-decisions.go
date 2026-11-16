package postgres

import (
	"context"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	insertModerationDecision = `INSERT INTO moderation_decisions
 (id, case_id, operator_id, operator_name, action, reason, note, revision, at, reason_message)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	listModerationDecisions = `SELECT d.id, d.case_id, d.operator_id, d.operator_name,
 d.action, d.reason, d.note, d.revision, d.at, d.reason_message
 FROM moderation_decisions d JOIN moderation_cases c ON c.id = d.case_id
 WHERE c.tenant_id = $1 AND c.product_id = $2 ORDER BY d.at DESC, d.id DESC`
	latestModerationDecisions = `SELECT DISTINCT ON (c.product_id)
 c.product_id, d.id, d.case_id, d.operator_id, d.operator_name,
 d.action, d.reason, d.note, d.revision, d.at, d.reason_message
 FROM moderation_decisions d JOIN moderation_cases c ON c.id = d.case_id
 WHERE c.tenant_id = $1 AND c.product_id = ANY($2::text[])
 ORDER BY c.product_id, d.at DESC, d.id DESC`
)

// ModerationDecisions не предоставляет операций изменения истории.
type ModerationDecisions struct {
	db *gorm.DB
}

// Add добавляет решение без изменения прежних записей.
func (r *ModerationDecisions) Add(ctx context.Context, d moderation.Decision) error {
	_, err := execSQL(
		ctx,
		r.db,
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
		d.ReasonMessage,
	)
	return err
}

// List ограничивает объём чтения для операторского сценария.
func (r *ModerationDecisions) List(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) ([]moderation.Decision, error) {
	rows, err := rawSQL(ctx, r.db, listModerationDecisions, tenant, product).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
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
			&d.ReasonMessage,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

// Latest читает только последнее решение каждого товара, не загружая весь журнал.
func (r *ModerationDecisions) Latest(
	ctx context.Context,
	tenant ids.TenantID,
	products []ids.ProductID,
) (map[ids.ProductID]moderation.Decision, error) {
	result := make(map[ids.ProductID]moderation.Decision)
	if len(products) == 0 {
		return result, nil
	}
	rows, err := rawSQL(ctx, r.db, latestModerationDecisions, tenant, products).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		product, d, err := scanLatestModerationDecision(rows)
		if err != nil {
			return nil, err
		}
		result[product] = d
	}
	return result, rows.Err()
}

func scanLatestModerationDecision(row rowScanner) (ids.ProductID, moderation.Decision, error) {
	var product ids.ProductID
	var d moderation.Decision
	err := row.Scan(
		&product,
		&d.ID,
		&d.CaseID,
		&d.Operator.ID,
		&d.Operator.Name,
		&d.Action,
		&d.Reason,
		&d.Note,
		&d.Revision,
		&d.At,
		&d.ReasonMessage,
	)
	return product, d, err
}
