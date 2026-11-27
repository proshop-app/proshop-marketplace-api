package jobs

import (
	"context"
	"slices"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	// WorkRecovery ограничивает ожидание потерянной подсказки.
	WorkRecovery        = 30 * time.Second
	workContentionDelay = 25 * time.Millisecond
	workMaxDelay        = time.Second
)

type workDeadline func(context.Context) (time.Time, error)

type workRetry struct {
	delay time.Duration
	until time.Time
}

type workEntry struct {
	at    time.Time
	retry workRetry
}

type workSchedule map[string]workEntry

func (r *workRetry) afterAttempt(now time.Time, claimed bool, err error) {
	switch {
	case err != nil:
		r.until = now.Add(workMaxDelay)
	case claimed:
		*r = workRetry{}
	default:
		r.delay = min(workMaxDelay, max(workContentionDelay, 2*r.delay))
		r.until = now.Add(r.delay)
	}
}

func (s workSchedule) signal(kind string, at time.Time) {
	s[kind] = workEntry{at: earlierTime(at, s[kind].at)}
}

func (s workSchedule) update(kinds []string, deadlines map[string]time.Time) {
	for kind := range s {
		if deadlines[kind].IsZero() && (len(kinds) == 0 || slices.Contains(kinds, kind)) {
			delete(s, kind)
		}
	}
	for kind, at := range deadlines {
		entry := s[kind]
		entry.at = at
		s[kind] = entry
	}
}

func (s workSchedule) delay(kinds []string, at time.Time) {
	for kind, entry := range s {
		if len(kinds) == 0 || slices.Contains(kinds, kind) {
			entry.retry.until = laterTime(entry.retry.until, at)
			s[kind] = entry
		}
	}
}

func (s workSchedule) afterAttempt(kinds, claimed []string, now time.Time, err error) {
	for _, kind := range kinds {
		if entry, ok := s[kind]; ok {
			entry.retry.afterAttempt(now, slices.Contains(claimed, kind), err)
			s[kind] = entry
		}
	}
}

func (s workSchedule) nearest() time.Time {
	var nearest time.Time
	for _, entry := range s {
		nearest = earlierTime(nearest, laterTime(entry.at, entry.retry.until))
	}
	return nearest
}

func (s workSchedule) due(now time.Time) []string {
	var kinds []string
	for kind, entry := range s {
		if isDue(laterTime(entry.at, entry.retry.until), now) {
			kinds = append(kinds, kind)
		}
	}
	slices.Sort(kinds)
	return kinds
}

func isDue(at, now time.Time) bool {
	return !at.IsZero() && !at.After(now)
}

// waitWork ждёт подсказку, реальный срок или страховочную сверку.
func waitWork(
	ctx context.Context, clock ports.Clock, next time.Time, watch ports.WorkWatch, recovery <-chan time.Time,
) bool {
	ticks, stop := workTimer(ctx, clock, next)
	defer stop()
	select {
	case <-ctx.Done():
		return false
	case <-watch.Changed():
		watch.Take()
	case <-ticks:
	case <-recovery:
	}
	return ctx.Err() == nil
}

func workTimer(ctx context.Context, clock ports.Clock, next time.Time) (<-chan struct{}, func()) {
	if next.IsZero() {
		return nil, func() {}
	}
	timer, cancel := ports.WithTimeout(ctx, clock, next.Sub(clock.Now()))
	return timer.Done(), cancel
}

func laterTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}

func earlierTime(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}
