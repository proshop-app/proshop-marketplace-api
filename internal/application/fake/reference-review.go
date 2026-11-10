package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CandidateEntryIDs — тот же ограниченный выбор только кандидатов без исключений.
func (r references) CandidateEntryIDs(
	_ context.Context,
	dictionary string,
	excluded []ids.UUID,
	limit int,
) ([]ids.UUID, error) {
	omitted := make(map[ids.UUID]bool, len(excluded))
	for _, id := range excluded {
		omitted[id] = true
	}
	selected := []ids.UUID{}
	for _, entry := range r.sorted(dictionary, reference.StatusCandidate) {
		if !omitted[entry.ID()] {
			selected = append(selected, entry.ID())
			if len(selected) == limit {
				break
			}
		}
	}
	return selected, nil
}
