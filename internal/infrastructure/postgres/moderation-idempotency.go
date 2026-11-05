package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

const (
	lockModerationDecision   = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
	selectModerationDecision = `SELECT response FROM moderation_idempotency WHERE key_hash = $1 AND expires_at > $2`
	saveModerationDecision   = `INSERT INTO moderation_idempotency (key_hash, response, expires_at) VALUES ($1, $2, $3)
 ON CONFLICT (key_hash) DO UPDATE
 SET response = EXCLUDED.response,
     expires_at = EXCLUDED.expires_at`
)

// Cached сериализует повторы до проверки версии и удерживает блокировку до commit.
func (p *ModerationDecisions) Cached(
	ctx context.Context,
	key string,
	now time.Time,
) (moderation.CachedDecision, bool, error) {
	var value moderation.CachedDecision
	if _, err := p.q.Exec(ctx, lockModerationDecision, key); err != nil {
		return value, false, err
	}
	var raw []byte
	err := p.q.QueryRow(ctx, selectModerationDecision, key, now).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, false, nil
	}
	if err != nil {
		return value, false, err
	}
	err = json.Unmarshal(raw, &value)
	return value, err == nil, err
}

// Cache сохраняется в той же транзакции, что переход, аудит и outbox.
func (p *ModerationDecisions) Cache(ctx context.Context, key string, decision moderation.CachedDecision) error {
	raw, err := json.Marshal(decision)
	if err != nil {
		return err
	}
	_, err = p.q.Exec(ctx, saveModerationDecision, key, raw, decision.ExpiresAt)
	return err
}
