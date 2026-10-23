package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	lockEventDecision   = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
	selectEventDecision = `SELECT response FROM operator_event_idempotency WHERE key_hash = $1 AND expires_at > $2`
	saveEventDecision   = `INSERT INTO operator_event_idempotency (key_hash, response, expires_at) VALUES ($1, $2, $3)
 ON CONFLICT (key_hash) DO UPDATE
 SET response = EXCLUDED.response,
     expires_at = EXCLUDED.expires_at`
)

// Decision сериализует повторы до проверки версии и удерживает блокировку до commit.
func (p *Parked) Decision(ctx context.Context, key string, now time.Time) (ports.EventDecision, bool, error) {
	var value ports.EventDecision
	if _, err := p.q.Exec(ctx, lockEventDecision, key); err != nil {
		return value, false, err
	}
	var raw []byte
	err := p.q.QueryRow(ctx, selectEventDecision, key, now).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, false, nil
	}
	if err != nil {
		return value, false, err
	}
	err = json.Unmarshal(raw, &value)
	return value, err == nil, err
}

// SaveDecision сохраняется в той же транзакции, что переход, аудит и outbox.
func (p *Parked) SaveDecision(ctx context.Context, key string, decision ports.EventDecision) error {
	raw, err := json.Marshal(decision)
	if err != nil {
		return err
	}
	_, err = p.q.Exec(ctx, saveEventDecision, key, raw, decision.ExpiresAt)
	return err
}
