package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// insertChange — запись журнала первой в изменении: её ревизия становится
	// ревизией затронутых записей и головой словаря. Подсчёт closed
	// закрывает прежние версии до вставки в частичный уникальный индекс.
	insertChange = `
		WITH change AS (
			INSERT INTO reference.changes (
				dictionary_code, entry_id, entry_ids, kind, actor, source, reason, reverts, forward, inverse, at
			)
			VALUES ($1, $2, $3::uuid[], $4, $5, $6, $7, $8, $9, $10, $11)
			RETURNING revision, dictionary_code, entry_ids, forward
		), closed AS (
			UPDATE reference.snapshot_versions v
			SET replaced_at = c.revision
			FROM change c
			WHERE v.dictionary_code = c.dictionary_code AND v.entry_id = ANY(c.entry_ids) AND v.replaced_at IS NULL
			RETURNING v.position
		), images AS (
			INSERT INTO reference.snapshot_versions (dictionary_code, entry_id, revision, state)
			SELECT dictionary_code, (image->>'id')::uuid, revision, NULLIF(image->'state', 'null'::jsonb)
			FROM change CROSS JOIN LATERAL jsonb_array_elements(forward) AS images(image)
			WHERE (SELECT count(*) FROM closed) >= 0
		)
		SELECT revision FROM change`

	selectChanges = `
		SELECT revision, dictionary_code, entry_id, entry_ids, kind, actor, source, reason,
			COALESCE(reverts, 0), forward, inverse, at
		FROM reference.changes
		WHERE dictionary_code = $1 AND revision > $2 AND revision <= $3
		ORDER BY revision`

	selectSnapshotSinceExpired = `
		SELECT EXISTS (
			SELECT 1 FROM reference.changes
			WHERE dictionary_code = $1 AND revision > $2 AND revision <= $3 AND at < $4
		)`

	selectRegistryChangedSince = `
		SELECT EXISTS (
			SELECT 1 FROM reference.changes
			WHERE dictionary_code = $1 AND revision > $2 AND kind <> $3
		)`
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
	err = rawSQL(
		ctx,
		r.db,
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
	).Row().Scan(&revision)
	if err != nil {
		return 0, fmt.Errorf("журнал %s: %w", change.Dictionary, err)
	}
	return revision, nil
}

// RegistryChangedSince отличает правки реестра от восстановления образов.
func (r *References) RegistryChangedSince(ctx context.Context, dictionary string, since int64) (bool, error) {
	var changed bool
	err := rawSQL(
		ctx,
		r.db,
		selectRegistryChangedSince,
		dictionary,
		since,
		string(reference.ChangeSnapshotBackfilled),
	).Row().Scan(&changed)
	return changed, err
}

// Changes — записи журнала словаря в (from, to].
func (r *References) Changes(ctx context.Context, dictionary string, from, to int64) ([]reference.Change, error) {
	rows, err := rawSQL(ctx, r.db, selectChanges, dictionary, from, to).Rows()
	if err != nil {
		return nil, fmt.Errorf("журнал %s: %w", dictionary, err)
	}
	defer func() {
		_ = rows.Close()
	}()
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
			pgtype.NewMap().SQLScanner(&entries),
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
		change.EntryID, change.Kind = ids.UUID(entry), reference.ChangeKind(kind)
		change.Meta.Source = reference.Source(source)
		for _, id := range entries {
			change.EntryIDs = append(change.EntryIDs, ids.UUID(id))
		}
		out = append(out, change)
	}
	return out, rows.Err()
}

// SnapshotSinceExpired проверяет, пропустил ли потребитель изменения старше горизонта.
func (r *References) SnapshotSinceExpired(
	ctx context.Context,
	dictionary string,
	since, revision int64,
	before time.Time,
) (bool, error) {
	var expired bool
	err := rawSQL(ctx, r.db, selectSnapshotSinceExpired, dictionary, since, revision, before).Row().Scan(&expired)
	return expired, err
}
