package postgres

import (
	"context"

	"gorm.io/gorm/clause"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

const (
	moderationReasonsTable = "moderation_reasons"
	moderationReasonByCode = "code = ?"
)

// ListReasons возвращает операторский словарь без встроенных тематик.
func (r *ModerationRules) ListReasons(ctx context.Context) ([]moderation.ReasonCode, error) {
	result := []moderation.ReasonCode{}
	err := r.db.WithContext(ctx).Table(moderationReasonsTable).Order("code").Find(&result).Error

	return result, err
}

// PutReason проверяет код и текст до записи словаря.
func (r *ModerationRules) PutReason(ctx context.Context, reason moderation.ReasonCode) error {
	if err := reason.Validate(); err != nil {
		return err
	}
	result := r.db.WithContext(ctx).
		Table(moderationReasonsTable).
		Where(moderationReasonByCode, reason.Code).
		Update("text", reason.Text)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return moderation.ErrNotFound
	}
	return nil
}

// AddReason не перезаписывает занятый код при одновременном создании.
func (r *ModerationRules) AddReason(ctx context.Context, reason moderation.ReasonCode) error {
	if err := reason.Validate(); err != nil {
		return err
	}
	result := r.db.WithContext(ctx).
		Table(moderationReasonsTable).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(map[string]any{
			"code": reason.Code,
			"text": reason.Text,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return moderation.ErrReasonExists
	}
	return nil
}
