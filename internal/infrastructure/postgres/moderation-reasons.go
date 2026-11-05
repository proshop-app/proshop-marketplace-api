package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

const (
	listModerationReasons = `SELECT code, text FROM moderation_reasons ORDER BY code`
	putModerationReason   = `INSERT INTO moderation_reasons (code, text) VALUES ($1, $2)
 ON CONFLICT (code) DO UPDATE SET text = EXCLUDED.text`
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
	_, err := r.q.Exec(ctx, putModerationReason, reason.Code, reason.Text)
	return err
}
