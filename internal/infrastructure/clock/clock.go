// Package clock — системное время с точностью хранилища.
package clock

import "time"

// System — текущее время в UTC с точностью до микросекунды: так оно совпадает
// с прочитанным из PostgreSQL и сравнения не расходятся.
type System struct{}

// Now — текущее время.
func (System) Now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}
