package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	acquireLease = `
		INSERT INTO job_leases (name, holder, until)
		VALUES ($1, $2, now() + make_interval(secs => $3))
		ON CONFLICT (name) DO UPDATE
		SET holder = EXCLUDED.holder,
			until = EXCLUDED.until
		WHERE job_leases.until < now() OR job_leases.holder = EXCLUDED.holder`

	releaseLease = `
		DELETE FROM job_leases
		WHERE name = $1 AND holder = $2`
)

// Leases — аренда фоновых задач по часам базы.
type Leases struct {
	pool *pgxpool.Pool
}

// NewLeases — аренда поверх пула.
func NewLeases(pool *pgxpool.Pool) *Leases {
	return &Leases{pool: pool}
}

// Acquire берёт свободную, истёкшую или свою аренду.
func (l *Leases) Acquire(ctx context.Context, name, holder string, ttl time.Duration) (bool, error) {
	tag, err := l.pool.Exec(ctx, acquireLease, name, holder, ttl.Seconds())
	if err != nil {
		return false, fmt.Errorf("аренда %s: %w", name, err)
	}

	return tag.RowsAffected() == 1, nil
}

// Release отпускает свою аренду.
func (l *Leases) Release(ctx context.Context, name, holder string) error {
	if _, err := l.pool.Exec(ctx, releaseLease, name, holder); err != nil {
		return fmt.Errorf("аренда %s: %w", name, err)
	}

	return nil
}
