package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

const (
	addModerationReason   = `INSERT INTO moderation_reasons (code, text) VALUES ($1, $2) ON CONFLICT DO NOTHING`
	listModerationReasons = `SELECT code, text FROM moderation_reasons ORDER BY code`
	putModerationReason   = `UPDATE moderation_reasons SET text = $2 WHERE code = $1`
)

// ListReasons возвращает операторский словарь без встроенных тематик.
func (r *ModerationRules) ListReasons(ctx context.Context) ([]moderation.ReasonCode, error) {
	rows, err := r.q.Query(ctx, listModerationReasons)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []moderation.ReasonCode{}
	for rows.Next() {
		var reason moderation.ReasonCode
		if err := rows.Scan(&reason.Code, &reason.Text); err != nil {
			return nil, err
		}
		result = append(result, reason)
	}
	return result, rows.Err()
}

// PutReason проверяет код и текст до записи словаря.
func (r *ModerationRules) PutReason(ctx context.Context, reason moderation.ReasonCode) error {
	if err := reason.Validate(); err != nil {
		return err
	}
	tag, err := r.q.Exec(ctx, putModerationReason, reason.Code, reason.Text)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return moderation.ErrNotFound
	}
	return nil
}

// AddReason не перезаписывает занятый код при одновременном создании.
func (r *ModerationRules) AddReason(ctx context.Context, reason moderation.ReasonCode) error {
	if err := reason.Validate(); err != nil {
		return err
	}
	tag, err := r.q.Exec(ctx, addModerationReason, reason.Code, reason.Text)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return moderation.ErrReasonExists
	}
	return nil
}
