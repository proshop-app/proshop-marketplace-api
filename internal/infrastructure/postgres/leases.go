package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
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
	source *connectionSource
}

// NewLeases — аренда поверх пула.
func NewLeases(pool *pgxpool.Pool) *Leases {
	return &Leases{source: newConnectionSource(pool)}
}

// Acquire берёт свободную, истёкшую или свою аренду.
func (l *Leases) Acquire(ctx context.Context, name, holder string, ttl time.Duration) (bool, error) {
	var acquired bool

	err := l.source.withConnection(ctx, func(db *gorm.DB) error {
		affected, err := execSQL(ctx, db, acquireLease, name, holder, ttl.Seconds())
		if err != nil {
			return fmt.Errorf("аренда %s: %w", name, err)
		}

		acquired = affected == 1
		return nil
	})

	return acquired, err
}

// Release отпускает свою аренду.
func (l *Leases) Release(ctx context.Context, name, holder string) error {
	return l.source.withConnection(ctx, func(db *gorm.DB) error {
		if _, err := execSQL(ctx, db, releaseLease, name, holder); err != nil {
			return fmt.Errorf("аренда %s: %w", name, err)
		}

		return nil
	})
}
