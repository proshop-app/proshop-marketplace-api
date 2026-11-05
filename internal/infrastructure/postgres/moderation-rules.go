package postgres

import (
	"context"

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
type ModerationRules struct{ q querier }

// Current согласует список фраз с ревизией правил.
func (r *ModerationRules) Current(ctx context.Context) (moderation.Rules, error) {
	rules := moderation.Rules{Phrases: []moderation.StopPhrase{}}
	if err := r.q.QueryRow(ctx, readModerationRules).Scan(&rules.Revision); err != nil {
		return rules, err
	}
	rows, err := r.q.Query(ctx, listModerationPhrases)
	if err != nil {
		return rules, err
	}
	defer rows.Close()
	for rows.Next() {
		var p moderation.StopPhrase
		if err := rows.Scan(&p.Key, &p.Text, &p.Operator.ID, &p.Operator.Name, &p.CreatedAt); err != nil {
			return rules, err
		}
		rules.Phrases = append(rules.Phrases, p)
	}
	return rules, rows.Err()
}
