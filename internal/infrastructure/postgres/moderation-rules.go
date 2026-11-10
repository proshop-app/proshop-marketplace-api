package postgres

import (
	"context"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

const (
	readModerationRules   = `SELECT revision FROM moderation_rules WHERE id = 1 FOR SHARE`
	lockModerationRules   = `SELECT revision FROM moderation_rules WHERE id = 1 FOR UPDATE`
	bumpModerationRules   = `UPDATE moderation_rules SET revision = revision + 1 WHERE id = 1`
	listModerationPhrases = `SELECT key, text, operator_id, operator_name, created_at
 FROM moderation_stop_phrases ORDER BY created_at, key`
)

// ModerationRules сериализует правки словаря общей строкой ревизии.
type ModerationRules struct {
	db *gorm.DB
}

// Current согласует список фраз с ревизией правил.
func (r *ModerationRules) Current(ctx context.Context) (moderation.Rules, error) {
	rules := moderation.Rules{Phrases: []moderation.StopPhrase{}}
	if err := rawSQL(ctx, r.db, readModerationRules).Row().Scan(&rules.Revision); err != nil {
		return rules, err
	}
	rows, err := rawSQL(ctx, r.db, listModerationPhrases).Rows()
	if err != nil {
		return rules, err
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var p moderation.StopPhrase
		if err := rows.Scan(&p.Key, &p.Text, &p.Operator.ID, &p.Operator.Name, &p.CreatedAt); err != nil {
			return rules, err
		}
		rules.Phrases = append(rules.Phrases, p)
	}
	return rules, rows.Err()
}
