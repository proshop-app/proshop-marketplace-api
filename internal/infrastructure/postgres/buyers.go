package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Buyers — покупатели в PostgreSQL.
type Buyers struct {
	q querier
}

func (r *Buyers) one(ctx context.Context, where string, arg any) (buyer.Buyer, error) {
	var (
		found     buyer.Buyer
		id, phone string
	)

	err := r.q.QueryRow(ctx, `SELECT id, phone, created_at FROM buyers WHERE `+where, arg).Scan(&id, &phone, &found.CreatedAt)
	if notFound(err) {
		return buyer.Buyer{}, buyer.ErrNotFound
	}
	if err != nil {
		return buyer.Buyer{}, fmt.Errorf("покупатель: %w", err)
	}

	found.ID, found.Phone = ids.UUID(id), buyer.Phone(phone)

	return found, nil
}

// FindByPhone — покупатель по телефону.
func (r *Buyers) FindByPhone(ctx context.Context, phone buyer.Phone) (buyer.Buyer, error) {
	return r.one(ctx, `phone = $1`, string(phone))
}

// Get — покупатель.
func (r *Buyers) Get(ctx context.Context, id ids.UUID) (buyer.Buyer, error) {
	return r.one(ctx, `id = $1`, string(id))
}

// Add заводит покупателя.
func (r *Buyers) Add(ctx context.Context, b buyer.Buyer) error {
	_, err := r.q.Exec(ctx, `INSERT INTO buyers (id, phone, created_at) VALUES ($1, $2, $3)`, string(b.ID), string(b.Phone), b.CreatedAt)
	if isUnique(err, "buyers_phone_key") {
		return buyer.ErrExists
	}
	if err != nil {
		return fmt.Errorf("новый покупатель: %w", err)
	}

	return nil
}

// Challenges — проверки входа в PostgreSQL.
type Challenges struct {
	q querier
}

const challengeColumns = `id, phone, provider_ref, call_phone, call_phone_pretty, status, checks, last_checked_at,
	expires_at, created_at, updated_at, version`

func (r *Challenges) one(ctx context.Context, where string, arg any) (*buyer.Challenge, error) {
	var (
		state             buyer.ChallengeState
		id, phone, status string
		lastChecked       *time.Time
	)

	err := r.q.QueryRow(ctx, `SELECT `+challengeColumns+` FROM phone_challenges WHERE `+where, arg).Scan(
		&id, &phone, &state.ProviderRef, &state.CallPhone, &state.CallPhonePretty, &status, &state.Checks, &lastChecked,
		&state.ExpiresAt, &state.CreatedAt, &state.UpdatedAt, &state.Version)
	if notFound(err) {
		return nil, buyer.ErrChallengeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("проверка входа: %w", err)
	}

	state.ID, state.Phone, state.Status = ids.UUID(id), buyer.Phone(phone), buyer.ChallengeStatus(status)
	if lastChecked != nil {
		state.LastCheckedAt = *lastChecked
	}

	return buyer.RestoreChallenge(state), nil
}

// Get — проверка.
func (r *Challenges) Get(ctx context.Context, id ids.UUID) (*buyer.Challenge, error) {
	return r.one(ctx, `id = $1`, string(id))
}

// FindByProviderRef — проверка по идентификатору шлюза.
func (r *Challenges) FindByProviderRef(ctx context.Context, ref string) (*buyer.Challenge, error) {
	return r.one(ctx, `provider_ref = $1`, ref)
}

// Add сохраняет новую проверку.
func (r *Challenges) Add(ctx context.Context, c *buyer.Challenge) error {
	s := c.State()

	_, err := r.q.Exec(ctx, `INSERT INTO phone_challenges (`+challengeColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		string(s.ID), string(s.Phone), s.ProviderRef, s.CallPhone, s.CallPhonePretty, string(s.Status), s.Checks,
		nullTime(s.LastCheckedAt), s.ExpiresAt, s.CreatedAt, s.UpdatedAt, s.Version)
	if err != nil {
		return fmt.Errorf("новая проверка входа: %w", err)
	}

	c.MarkSaved()

	return nil
}

// Update сохраняет условно по исходным статусу и версии.
func (r *Challenges) Update(ctx context.Context, c *buyer.Challenge) error {
	s := c.State()
	status, version := c.Origin()

	tag, err := r.q.Exec(ctx, `UPDATE phone_challenges
		SET status = $4, checks = $5, last_checked_at = $6, updated_at = $7, version = $8
		WHERE id = $1 AND status = $2 AND version = $3`,
		string(s.ID), string(status), version, string(s.Status), s.Checks, nullTime(s.LastCheckedAt), s.UpdatedAt, s.Version)
	if err != nil {
		return fmt.Errorf("проверка входа %s: %w", s.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return buyer.ErrChallengeConflict
	}

	c.MarkSaved()

	return nil
}
