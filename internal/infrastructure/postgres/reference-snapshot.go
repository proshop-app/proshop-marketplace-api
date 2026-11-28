package postgres

import (
	"context"
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

const (
	// При position > курсора расстояние от него растёт в том же порядке, что и position.
	// GiST обходит ближайшие подходящие версии без сортировки и чтения истории страницы.
	selectSnapshotEntries = `
		SELECT entry_id, position, revision, state
		FROM reference.snapshot_versions
		WHERE dictionary_code = $1 AND validity @> $2::bigint AND position > $3
			AND state IS NOT NULL AND state->>'status' = 'confirmed'
		ORDER BY position <-> $3
		LIMIT $4`

	selectSnapshotChanges = `
		SELECT entry_id, position, revision, state
		FROM reference.snapshot_versions
		WHERE dictionary_code = $1 AND revision > $2 AND validity @> $3::bigint AND position > $4
		ORDER BY position <-> $4
		LIMIT $5`
)

// SnapshotEntries читает подтверждённые записи на заданной ревизии.
func (r *References) SnapshotEntries(
	ctx context.Context,
	dictionary string,
	revision, after int64,
	limit int,
) ([]reference.SnapshotRow, error) {
	return r.snapshotRows(ctx, selectSnapshotEntries, dictionary, revision, after, limit)
}

// SnapshotChanges читает последние версии изменившихся записей, включая удаления.
func (r *References) SnapshotChanges(
	ctx context.Context,
	dictionary string,
	since, revision, after int64,
	limit int,
) ([]reference.SnapshotRow, error) {
	return r.snapshotRows(ctx, selectSnapshotChanges, dictionary, since, revision, after, limit)
}

func (r *References) snapshotRows(ctx context.Context, query string, args ...any) ([]reference.SnapshotRow, error) {
	rows, err := rawSQL(ctx, r.db, query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var page []reference.SnapshotRow
	for rows.Next() {
		var row reference.SnapshotRow
		var revision int64
		var raw []byte
		if err := rows.Scan(&row.ID, &row.Position, &revision, &raw); err != nil {
			return nil, err
		}
		if raw != nil {
			var state reference.EntryState
			if err := json.Unmarshal(raw, &state); err != nil {
				return nil, err
			}
			state.Revision = revision
			row.State = &state
		}
		page = append(page, row)
	}
	return page, rows.Err()
}
