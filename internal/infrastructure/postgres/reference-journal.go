package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// insertChange — запись журнала первой в изменении: её ревизия становится
	// ревизией затронутых записей и головой словаря.
	insertChange = `
		INSERT INTO reference.changes (
			dictionary_code, entry_id, entry_ids, kind, actor, source, reason, reverts, forward, inverse, at
		)
		VALUES ($1, $2, $3::uuid[], $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING revision`

	selectChanges = `
		SELECT revision, dictionary_code, entry_id, entry_ids, kind, actor, source, reason,
			COALESCE(reverts, 0), forward, inverse, at
		FROM reference.changes
		WHERE dictionary_code = $1 AND revision > $2 AND revision <= $3
		ORDER BY revision`

	// selectChangedEntries — затронутые записи в порядке первого касания.
	selectChangedEntries = `
		SELECT id
		FROM (
			SELECT unnest(entry_ids) AS id, revision
			FROM reference.changes
			WHERE dictionary_code = $1 AND revision > $2 AND revision <= $3
		) touched
		GROUP BY id
		ORDER BY min(revision), id`

	selectHorizon = `
		SELECT COALESCE(max(revision), 0)
		FROM reference.changes
		WHERE dictionary_code = $1 AND at < $2`
)

// Append пишет запись журнала и возвращает её ревизию.
func (r *References) Append(ctx context.Context, change reference.Change) (int64, error) {
	forward, err := json.Marshal(change.Forward)
	if err != nil {
		return 0, err
	}
	inverse, err := json.Marshal(change.Inverse)
	if err != nil {
		return 0, err
	}
	var reverts any
	if change.Reverts != 0 {
		reverts = change.Reverts
	}
	var revision int64
	err = r.q.QueryRow(
		ctx,
		insertChange,
		change.Dictionary,
		string(change.EntryID),
		uuidStrings(change.EntryIDs),
		string(change.Kind),
		change.Meta.Actor,
		string(change.Meta.Source),
		change.Meta.Reason,
		reverts,
		string(forward),
		string(inverse),
		change.At,
	).Scan(&revision)
	if err != nil {
		return 0, fmt.Errorf("журнал %s: %w", change.Dictionary, err)
	}
	return revision, nil
}

// Changes — записи журнала словаря в (from, to].
func (r *References) Changes(ctx context.Context, dictionary string, from, to int64) ([]reference.Change, error) {
	rows, err := r.q.Query(ctx, selectChanges, dictionary, from, to)
	if err != nil {
		return nil, fmt.Errorf("журнал %s: %w", dictionary, err)
	}
	defer rows.Close()
	var out []reference.Change
	for rows.Next() {
		var (
			change           reference.Change
			entry, kind      string
			source           string
			entries          []string
			forward, inverse []byte
		)
		err := rows.Scan(
			&change.Revision,
			&change.Dictionary,
			&entry,
			&entries,
			&kind,
			&change.Meta.Actor,
			&source,
			&change.Meta.Reason,
			&change.Reverts,
			&forward,
			&inverse,
			&change.At,
		)
		if err != nil {
			return nil, fmt.Errorf("журнал %s: %w", dictionary, err)
		}
		if err := json.Unmarshal(forward, &change.Forward); err != nil {
			return nil, fmt.Errorf("журнал %s, ревизия %d: %w", dictionary, change.Revision, err)
		}
		if err := json.Unmarshal(inverse, &change.Inverse); err != nil {
			return nil, fmt.Errorf("журнал %s, ревизия %d: %w", dictionary, change.Revision, err)
		}
		change.EntryID, change.Kind, change.Meta.Source = ids.UUID(entry), reference.ChangeKind(kind), reference.Source(source)
		for _, id := range entries {
			change.EntryIDs = append(change.EntryIDs, ids.UUID(id))
		}
		out = append(out, change)
	}
	return out, rows.Err()
}

// ChangedEntries — записи, затронутые ревизиями (since, until].
func (r *References) ChangedEntries(ctx context.Context, dictionary string, since, until int64) ([]ids.UUID, error) {
	rows, err := r.q.Query(ctx, selectChangedEntries, dictionary, since, until)
	if err != nil {
		return nil, fmt.Errorf("дельта %s: %w", dictionary, err)
	}
	defer rows.Close()
	var out []ids.UUID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, ids.UUID(id))
	}
	return out, rows.Err()
}

// Horizon — последняя ревизия словаря старше before.
func (r *References) Horizon(ctx context.Context, dictionary string, before time.Time) (int64, error) {
	var revision int64
	if err := r.q.QueryRow(ctx, selectHorizon, dictionary, before).Scan(&revision); err != nil {
		return 0, fmt.Errorf("горизонт журнала %s: %w", dictionary, err)
	}
	return revision, nil
}
