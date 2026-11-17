package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// RetryStats повторяет условие частичного индекса: первая аренда ещё не является повтором.
func (s *Store) RetryStats(_ context.Context) ([]ports.TaskRetryStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	byKind := map[string]ports.TaskRetryStats{}
	for _, row := range s.data.outbox {
		if row.DoneAt != nil || row.FailedAt != nil || (row.LastError == "" && row.UnknownFailures == 0) {
			continue
		}
		item := byKind[row.Kind]
		item.Kind = row.Kind
		item.Count++
		if item.Oldest.IsZero() || row.CreatedAt.Before(item.Oldest) {
			item.Oldest = row.CreatedAt
		}
		byKind[row.Kind] = item
	}
	result := make([]ports.TaskRetryStats, 0, len(byKind))
	for _, item := range byKind {
		result = append(result, item)
	}
	return result, nil
}
