// Package clock — системное время с точностью хранилища.
package clock

import (
	"context"
	"time"
)

// System — текущее время в UTC с точностью до микросекунды: так оно совпадает
// с прочитанным из PostgreSQL и сравнения не расходятся.
type System struct{}

// Now — текущее время.
func (System) Now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// Wait ждёт d по таймеру; false — контекст отменён раньше.
func (System) Wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
