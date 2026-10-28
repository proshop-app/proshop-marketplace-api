package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

const (
	lastRelease = `
        SELECT release, checksum, published_version, COALESCE(rolled_back_version, 0), previous_definition
        FROM taxonomy_releases
        ORDER BY release DESC
        LIMIT 1`
	insertRelease = `
        INSERT INTO taxonomy_releases
            (release, checksum, published_version, previous_definition)
        VALUES ($1, $2, $3, $4)`
	markTaxonomyRollback = `
        UPDATE taxonomy_releases
        SET rolled_back_version = $2,
            rolled_back_at = now()
        WHERE release = $1`
)

// LatestRelease вызывается после Lock: поставки и загрузка оператора сериализованы одной головой.
func (r *Taxonomy) LatestRelease(ctx context.Context) (*taxonomy.ReleaseRecord, error) {
	var record taxonomy.ReleaseRecord
	var raw []byte
	err := r.q.QueryRow(ctx, lastRelease).Scan(
		&record.Version,
		&record.Checksum,
		&record.PublishedVersion,
		&record.RolledBackVersion,
		&raw,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &record.Previous); err != nil {
		return nil, err
	}
	return &record, nil
}

// RecordRelease фиксируется той же транзакцией, что и новая голова таксономии.
func (r *Taxonomy) RecordRelease(
	ctx context.Context,
	release taxonomy.Release,
	version int64,
	previous taxonomy.Definition,
) error {
	raw, err := json.Marshal(previous)
	if err != nil {
		return err
	}
	_, err = r.q.Exec(ctx, insertRelease, release.Version, release.Checksum, version, raw)
	return err
}

// MarkRollback сохраняет версию восстановленного снимка в той же транзакции.
func (r *Taxonomy) MarkRollback(ctx context.Context, release, version int64) error {
	_, err := r.q.Exec(ctx, markTaxonomyRollback, release, version)
	return err
}
