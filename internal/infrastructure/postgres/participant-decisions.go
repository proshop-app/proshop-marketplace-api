package postgres

import (
	"context"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Значения статусов сохраняются в БД и проверяются ограничениями схемы.
const (
	insertParticipantDecision = `INSERT INTO participant_decisions
 (id, tenant_id, operator_id, operator_name, action, value, reason, note, at, reason_message, actor, previous_value)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	listParticipantDecisions = `SELECT id, tenant_id, operator_id, operator_name,
 action, value, reason, note, at, reason_message, actor, previous_value
 FROM participant_decisions WHERE tenant_id = $1 ORDER BY at DESC, id DESC LIMIT $2 OFFSET $3`
	countParticipantDecisions = `SELECT count(*) FROM participant_decisions WHERE tenant_id = $1`
)

// ParticipantDecisions не предоставляет операций изменения истории.
type ParticipantDecisions struct {
	db *gorm.DB
}

// Add добавляет решение без изменения прежних записей.
func (r *ParticipantDecisions) Add(ctx context.Context, d participant.DecisionRecord) error {
	if d.Actor == "" {
		d.Actor = "operator"
	}
	_, err := execSQL(
		ctx,
		r.db,
		insertParticipantDecision,
		d.ID,
		d.TenantID,
		d.Operator.ID,
		d.Operator.Name,
		d.Action,
		d.Value,
		d.Reason,
		d.Note,
		d.At,
		d.ReasonMessage,
		d.Actor,
		d.PreviousValue,
	)
	return err
}

// List ограничивает объём чтения для операторского сценария.
func (r *ParticipantDecisions) List(
	ctx context.Context,
	tenant ids.TenantID,
	page participant.Page,
) ([]participant.DecisionRecord, int, error) {
	var total int
	if err := rawSQL(ctx, r.db, countParticipantDecisions, tenant).Row().Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := rawSQL(ctx, r.db, listParticipantDecisions, tenant, page.Limit, page.Offset).Rows()
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := []participant.DecisionRecord{}
	for rows.Next() {
		var d participant.DecisionRecord
		err := rows.Scan(
			&d.ID,
			&d.TenantID,
			&d.Operator.ID,
			&d.Operator.Name,
			&d.Action,
			&d.Value,
			&d.Reason,
			&d.Note,
			&d.At,
			&d.ReasonMessage,
			&d.Actor,
			&d.PreviousValue,
		)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, d)
	}
	return result, total, rows.Err()
}
