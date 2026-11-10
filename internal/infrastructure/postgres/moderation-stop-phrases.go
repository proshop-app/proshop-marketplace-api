package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

const (
	insertModerationPhrase = `INSERT INTO moderation_stop_phrases (key, text, operator_id, operator_name, created_at)
 VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`
	deleteModerationPhrase = `DELETE FROM moderation_stop_phrases WHERE key = $1`
)

// AddPhrase увеличивает ревизию только после добавления уникального ключа.
func (r *ModerationRules) AddPhrase(ctx context.Context, p moderation.StopPhrase) error {
	var revision int64
	if err := rawSQL(ctx, r.db, lockModerationRules).Row().Scan(&revision); err != nil {
		return err
	}
	affected, err := execSQL(
		ctx,
		r.db,
		insertModerationPhrase,
		p.Key,
		p.Text,
		p.Operator.ID,
		p.Operator.Name,
		p.CreatedAt,
	)
	if err != nil {
		return err
	}
	if affected == 0 {
		return moderation.ErrPhraseExists
	}
	_, err = execSQL(ctx, r.db, bumpModerationRules)
	return err
}

// RemovePhrase увеличивает ревизию только после фактического удаления.
func (r *ModerationRules) RemovePhrase(ctx context.Context, key string) (bool, error) {
	var revision int64
	if err := rawSQL(ctx, r.db, lockModerationRules).Row().Scan(&revision); err != nil {
		return false, err
	}
	affected, err := execSQL(ctx, r.db, deleteModerationPhrase, key)
	if err != nil || affected == 0 {
		return false, err
	}
	_, err = execSQL(ctx, r.db, bumpModerationRules)
	return true, err
}
