package postgres

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	claimScheduledWork = `INSERT INTO job_leases (name, holder, until)
    VALUES ($1, 'scheduled', now() + make_interval(secs => $2))
    ON CONFLICT (name) DO UPDATE SET until = EXCLUDED.until, holder = EXCLUDED.holder
    WHERE job_leases.until <= now()`

	// intervalTaken — интервал ещё действует: реплика узнаёт это чтением, без пишущей команды.
	intervalTaken = `SELECT EXISTS (SELECT 1 FROM job_leases WHERE name = $1 AND until > now())`

	// releaseScheduledWork возвращает интервал: следующая попытка любой реплики повторит проход.
	releaseScheduledWork = `UPDATE job_leases SET until = now() WHERE name = $1`
)

// ScheduledWork сочетает исключительное владение с общим интервалом между стартами реплик.
// Advisory lock не даёт истёкшему интервалу запустить второй долгий проход.
type ScheduledWork struct {
	lock     *IndexRebuildLock
	source   *connectionSource
	name     string
	interval time.Duration
}

// SharedInterval — общий интервал между стартами реплик без блокировки прохода: для короткого
// прохода, которому пересечение с соседним не вредит (его строки берутся FOR UPDATE SKIP LOCKED).
// Работает только реплика, взявшая интервал, и ей не нужен прямой сеанс в обход пулера.
type SharedInterval struct {
	source   *connectionSource
	name     string
	interval time.Duration
}

// NewScheduledWork привязывает общее расписание к часам базы, без ожидания занятого
// владельца: интервал берётся пулом класса, блокировка прохода — прямым сеансом.
func NewScheduledWork(pool *ClassPool, sessions *DirectSessions, name string, interval time.Duration) *ScheduledWork {
	return &ScheduledWork{
		lock:     &IndexRebuildLock{sessions: sessions, name: name},
		source:   newConnectionSource(pool),
		name:     name,
		interval: interval,
	}
}

// WithLock пропускает уже выполненный интервал, включая старты реплик с разными фазами таймера.
// Интервал берётся первым обычным запросом пула: реплика, которой интервал не достался,
// не открывает прямой сеанс под advisory lock. Его открывает только тот, кто
// работает, и только на время прохода; блокировка не даёт следующему интервалу запустить
// второй долгий проход, пока идёт прежний.
func (s *ScheduledWork) WithLock(ctx context.Context, run func(context.Context) error) error {
	claimed, err := s.claim(ctx)
	if err != nil {
		return err
	}
	if !claimed {
		return ports.ErrRebuildBusy
	}
	return s.lock.WithLock(ctx, run)
}

func (s *ScheduledWork) claim(ctx context.Context) (bool, error) {
	return claimInterval(ctx, s.source, s.name, s.interval)
}

// NewSharedInterval — общий интервал прохода name на пуле класса.
func NewSharedInterval(pool *ClassPool, name string, interval time.Duration) *SharedInterval {
	return &SharedInterval{source: newConnectionSource(pool), name: name, interval: interval}
}

// Run выполняет проход, только если реплика взяла интервал; иначе — ErrIntervalTaken без прохода.
func (s *SharedInterval) Run(ctx context.Context, pass func(context.Context) error) error {
	claimed, err := claimInterval(ctx, s.source, s.name, s.interval)
	if err != nil {
		return err
	}
	if !claimed {
		return ports.ErrIntervalTaken
	}
	return pass(ctx)
}

// Release возвращает интервал прохода, прерванного не по его вине: следующая попытка повторит его.
func (s *ScheduledWork) Release(ctx context.Context) error {
	return s.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		_, err := execSQL(ctx, db, releaseScheduledWork, s.name)
		return err
	})
}

// claimInterval — интервал берёт первая реплика после его истечения по часам базы. Действующий
// интервал реплика узнаёт чтением: холостая попытка не пишет в базу.
func claimInterval(ctx context.Context, source *connectionSource, name string, interval time.Duration) (bool, error) {
	var claimed bool
	err := source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		var taken bool
		if err := rawSQL(ctx, db, intervalTaken, name).Row().Scan(&taken); err != nil || taken {
			return err
		}
		affected, err := execSQL(ctx, db, claimScheduledWork, name, interval.Seconds())
		claimed = affected > 0
		return err
	})
	return claimed, err
}
