package jobs

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	shortSchedulesLease = "short-schedules"
	shortSchedulesStep  = 30 * time.Second
	shortSchedulesTTL   = 45 * time.Second
)

// ScheduleLease хранит владельца коротких расписаний по часам базы.
type ScheduleLease interface {
	Acquire(context.Context, string, string, time.Duration) (bool, error)
}

// ShortSchedules обновляет одну аренду. Каждый короткий проход проверяет её в памяти.
type ShortSchedules struct {
	lease  ScheduleLease
	holder string
	clock  ports.Clock
	logger *slog.Logger
	until  atomic.Pointer[time.Time]
}

// NewShortSchedules подключает расписания процесса к общей аренде.
func NewShortSchedules(lease ScheduleLease, holder string, clock ports.Clock, logger *slog.Logger) *ShortSchedules {
	return &ShortSchedules{lease: lease, holder: holder, clock: clock, logger: logger}
}

// Refresh получает или продлевает аренду до ближайшей проверки.
func (s *ShortSchedules) Refresh(ctx context.Context) {
	until := s.clock.Now().Add(shortSchedulesTTL)
	owned, err := s.lease.Acquire(ctx, shortSchedulesLease, s.holder, shortSchedulesTTL)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			s.logger.Error("аренда коротких расписаний недоступна", slog.Any("error", err))
		}
	case owned:
		s.until.Store(&until)
	default:
		s.until.Store(nil)
	}
}

// Renew проверяет аренду с одним интервалом для всех расписаний.
func (s *ShortSchedules) Renew(ctx context.Context) error {
	defer s.until.Store(nil)
	for s.clock.Wait(ctx, shortSchedulesStep) {
		s.Refresh(ctx)
	}
	return nil
}

// Run допускает короткий проход по уже полученной аренде.
func (s *ShortSchedules) Run(ctx context.Context, pass func(context.Context) error) error {
	if until := s.until.Load(); until == nil || !s.clock.Now().Before(*until) {
		return ports.ErrIntervalTaken
	}
	return pass(ctx)
}
