package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const claimScheduledWork = `INSERT INTO job_leases (name, holder, until)
    VALUES ($1, 'scheduled', now() + make_interval(secs => $2))
    ON CONFLICT (name) DO UPDATE SET until = EXCLUDED.until, holder = EXCLUDED.holder
    WHERE job_leases.until <= now()`

// ScheduledWork сочетает исключительное владение с общим интервалом между стартами реплик.
// Advisory lock не даёт истёкшему интервалу запустить второй долгий проход.
type ScheduledWork struct {
	lock     *IndexRebuildLock
	source   *connectionSource
	name     string
	interval time.Duration
}

// NewScheduledWork привязывает общее расписание к часам базы, без ожидания занятого владельца.
func NewScheduledWork(pool *pgxpool.Pool, name string, interval time.Duration) *ScheduledWork {
	return &ScheduledWork{
		lock:     &IndexRebuildLock{source: newConnectionSource(pool), name: name},
		source:   newConnectionSource(pool),
		name:     name,
		interval: interval,
	}
}

// WithLock пропускает уже выполненный интервал, включая старты реплик с разными фазами таймера.
func (s *ScheduledWork) WithLock(ctx context.Context, run func(context.Context) error) error {
	return s.lock.WithLock(ctx, func(ctx context.Context) error {
		var claimed bool
		err := s.source.withConnection(ctx, func(db *gorm.DB) error {
			affected, err := execSQL(ctx, db, claimScheduledWork, s.name, s.interval.Seconds())
			claimed = affected > 0
			return err
		})
		if err != nil {
			return err
		}
		if !claimed {
			return ports.ErrRebuildBusy
		}
		return run(ctx)
	})
}
