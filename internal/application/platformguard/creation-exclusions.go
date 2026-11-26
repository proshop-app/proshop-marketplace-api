package platformguard

import (
	"container/heap"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// creationExclusions хранит только молчащие магазины и магазины с открытым размыкателем.
// Действующая проба не истекает по часам и стоит после магазинов с известным сроком.
type creationExclusions []*tenantState

func (q creationExclusions) Len() int { return len(q) }

func (q creationExclusions) Less(i, j int) bool {
	left, right := q[i], q[j]
	if left.breaker.probe != right.breaker.probe {
		return !left.breaker.probe
	}
	return left.exclusionUntil().Before(right.exclusionUntil())
}

func (q creationExclusions) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].exclusionPosition = i + 1
	q[j].exclusionPosition = j + 1
}

func (q *creationExclusions) Push(value any) {
	state := value.(*tenantState)
	*q = append(*q, state)
	state.exclusionPosition = len(*q)
}

func (q *creationExclusions) Pop() any {
	last := len(*q) - 1
	state := (*q)[last]
	(*q)[last] = nil
	*q = (*q)[:last]
	state.exclusionPosition = 0
	return state
}

func (s *tenantState) exclusionUntil() time.Time {
	if s.breaker.openUntil.After(s.silentUntil) {
		return s.breaker.openUntil
	}
	return s.silentUntil
}

func (s *tenantState) excludedAt(now time.Time) bool {
	return s.breaker.probe || now.Before(s.exclusionUntil())
}

// updateCreationExclusion вызывается под общим мьютексом после изменения размыкателя или срока молчания.
func (c *Client) updateCreationExclusion(state *tenantState, now time.Time) {
	pos := state.exclusionPosition
	if !state.excludedAt(now) {
		if pos != 0 {
			heap.Remove(&c.excluded, pos-1)
		}
		return
	}
	if pos == 0 {
		heap.Push(&c.excluded, state)
		return
	}
	heap.Fix(&c.excluded, pos-1)
}

func (c *Client) creationExcluded(now time.Time) []ids.TenantID {
	for len(c.excluded) > 0 && !c.excluded[0].excludedAt(now) {
		heap.Pop(&c.excluded)
	}
	if len(c.excluded) == 0 {
		return nil
	}
	excluded := make([]ids.TenantID, 0, len(c.excluded))
	for _, state := range c.excluded {
		excluded = append(excluded, state.id)
	}
	return excluded
}
