package fake

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// Resolve сохраняет пачку только при совпадении всех токенов владельца.
func (s *Store) Resolve(_ context.Context, results []ports.TaskResolution) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	positions := make([]int, 0, len(results))
	seen := map[int64]bool{}
	for _, result := range results {
		position := -1
		for n, row := range s.data.outbox {
			if row.ID == result.ID && row.Attempts == result.Attempts && result.Attempts > 0 && row.DoneAt == nil {
				position = n
				break
			}
		}
		if position < 0 || seen[result.ID] {
			return ports.ErrTaskClaimConflict
		}
		seen[result.ID] = true
		positions = append(positions, position)
	}
	for n, result := range results {
		row := &s.data.outbox[positions[n]]
		row.LastError = result.Error
		if result.NextAt == nil {
			row.DoneAt = &result.At
		} else {
			row.NextAt = *result.NextAt
		}
	}
	return nil
}

// ReadyCounts считает только готовые задачи.
func (s *Store) ReadyCounts(_ context.Context, now time.Time) (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	counts := map[string]int64{}
	for _, row := range s.data.outbox {
		if row.DoneAt == nil && !row.NextAt.After(now) {
			counts[row.Kind]++
		}
	}
	return counts, nil
}
