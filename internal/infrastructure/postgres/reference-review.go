package postgres

import (
	"context"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const selectReviewCandidates = `
    SELECT id
    FROM reference.entries
    WHERE dictionary_code = $1 AND status = 'candidate' AND NOT (id = ANY($2::uuid[]))
    ORDER BY id
    LIMIT $3`

// CandidateEntryIDs читает ограниченный выбор под уже взятой блокировкой словаря.
func (r *References) CandidateEntryIDs(
	ctx context.Context,
	dictionary string,
	excluded []ids.UUID,
	limit int,
) ([]ids.UUID, error) {
	rows, err := rawSQL(ctx, r.db, selectReviewCandidates, dictionary, uuidStrings(excluded), limit).Rows()
	if err != nil {
		return nil, fmt.Errorf("кандидаты словаря %s: %w", dictionary, err)
	}
	defer func() { _ = rows.Close() }()
	selected := []ids.UUID{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		selected = append(selected, ids.UUID(id))
	}
	return selected, rows.Err()
}
