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
			if row.ID == result.ID && row.Attempts == result.Attempts && result.Attempts > 0 &&
				row.DoneAt == nil && row.FailedAt == nil {
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
		row.UnknownFailures = result.UnknownFailures
		row.LastError = result.Error
		switch {
		case result.FailureReason != "":
			row.FailedAt = &result.At
			row.FailureReason = result.FailureReason
		case result.NextAt == nil:
			row.DoneAt = &result.At
		default:
			row.NextAt = *result.NextAt
			if result.PauseKey != "" && result.PauseUntil != nil &&
				result.PauseUntil.After(s.lanePauses[result.PauseKey]) {
				if s.lanePauses == nil {
					s.lanePauses = make(map[string]time.Time)
				}
				s.lanePauses[result.PauseKey] = *result.PauseUntil
			}
		}
	}
	return nil
}

// ReadyLanes считает магазины с созревшей задачей и срок старейшей по видам.
func (s *Store) ReadyLanes(_ context.Context, now time.Time) ([]ports.ReadyLane, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tenants := map[string]map[string]bool{}
	oldest := map[string]time.Time{}
	for _, row := range s.data.outbox {
		if row.DoneAt != nil || row.FailedAt != nil || row.NextAt.After(now) {
			continue
		}
		if tenants[row.Kind] == nil {
			tenants[row.Kind] = map[string]bool{}
		}
		tenants[row.Kind][string(row.TenantID)] = true
		if current, ok := oldest[row.Kind]; !ok || row.NextAt.Before(current) {
			oldest[row.Kind] = row.NextAt
		}
	}
	lanes := []ports.ReadyLane{}
	for kind, set := range tenants {
		lanes = append(lanes, ports.ReadyLane{Kind: kind, Tenants: int64(len(set)), Oldest: oldest[kind]})
	}
	return lanes, nil
}

// ReadyTasks — созревшие задачи по видам для проверок тестов; порт отдаёт только полосы.
func (s *Store) ReadyTasks(now time.Time) map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()

	counts := map[string]int{}
	for _, row := range s.data.outbox {
		if row.DoneAt == nil && row.FailedAt == nil && !row.NextAt.After(now) {
			counts[row.Kind]++
		}
	}
	return counts
}

// Failures читает сохранённые отказы по id.
func (s *Store) Failures(_ context.Context, after int64, limit int) ([]ports.TaskFailure, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []ports.TaskFailure{}
	for _, row := range s.data.outbox {
		if row.FailedAt != nil && row.ID > after && len(result) < limit {
			result = append(result, ports.TaskFailure{
				ID:        row.ID,
				Kind:      row.Kind,
				TenantID:  row.TenantID,
				Attempts:  row.Attempts,
				Reason:    row.FailureReason,
				FailedAt:  *row.FailedAt,
				CreatedAt: row.CreatedAt,
			})
		}
	}
	return result, nil
}

// Requeue повышает токен аренды и возобновляет бюджет только неизвестных ошибок.
func (s *Store) Requeue(_ context.Context, request ports.TaskRequeue) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := []int64{}
	for i := range s.data.outbox {
		row := &s.data.outbox[i]
		if row.FailedAt == nil || row.ID <= request.After || len(items) == request.Limit ||
			(request.ID > 0 && row.ID != request.ID) || (request.Kind != "" && row.Kind != request.Kind) {
			continue
		}
		row.Attempts++
		row.FailedAt = nil
		row.FailureReason = ""
		row.UnknownFailures = 0
		row.LastError = ""
		row.NextAt = request.At
		items = append(items, row.ID)
	}
	return items, nil
}
