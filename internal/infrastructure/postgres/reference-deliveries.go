package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	selectLatestDelivery = `
		SELECT dictionary_code, version, checksum, revision_from, revision_to, applied_at,
			rolled_back_at IS NOT NULL, COALESCE(rollback_revision, 0)
		FROM reference.deliveries
		WHERE dictionary_code = $1
		ORDER BY version DESC
		LIMIT 1`

	insertDelivery = `
		INSERT INTO reference.deliveries (dictionary_code, version, checksum, revision_from, revision_to, applied_at)
		VALUES ($1, $2, $3, $4, $5, $6)`

	markDeliveryRollback = `
		UPDATE reference.deliveries
		SET rolled_back_at = now(),
			rollback_revision = $3
		WHERE dictionary_code = $1 AND version = $2 AND rolled_back_at IS NULL`

	deletePresence = `
		DELETE FROM reference.entry_presence
		WHERE entry_id = $1`

	insertPresence = `
		INSERT INTO reference.entry_presence (entry_id, category_id, offers, sellers, computed_at)
		SELECT $1, category_id, offers, sellers, $5
		FROM unnest($2::text[], $3::integer[], $4::integer[]) AS rows (category_id, offers, sellers)`
)

// LatestDelivery — последняя по номеру поставка словаря; nil — поставок не было.
func (r *References) LatestDelivery(ctx context.Context, dictionary string) (*reference.DeliveryRecord, error) {
	var record reference.DeliveryRecord
	err := r.q.QueryRow(ctx, selectLatestDelivery, dictionary).Scan(
		&record.Dictionary,
		&record.Version,
		&record.Checksum,
		&record.RevisionFrom,
		&record.RevisionTo,
		&record.AppliedAt,
		&record.RolledBack,
		&record.RollbackRevision,
	)
	if notFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("поставки %s: %w", dictionary, err)
	}
	return &record, nil
}

// RecordDelivery фиксируется той же транзакцией, что и записи поставки.
func (r *References) RecordDelivery(ctx context.Context, record reference.DeliveryRecord) error {
	_, err := r.q.Exec(
		ctx,
		insertDelivery,
		record.Dictionary,
		record.Version,
		record.Checksum,
		record.RevisionFrom,
		record.RevisionTo,
		record.AppliedAt,
	)
	if isUnique(err, "") {
		return reference.ErrDeliveryConflict
	}
	if err != nil {
		return fmt.Errorf("поставка %s/%d: %w", record.Dictionary, record.Version, err)
	}
	return nil
}

// MarkDeliveryRollback отмечает откат той же транзакцией, что и обратные ревизии.
func (r *References) MarkDeliveryRollback(ctx context.Context, dictionary string, version, revision int64) error {
	tag, err := r.q.Exec(ctx, markDeliveryRollback, dictionary, version, revision)
	if err != nil {
		return fmt.Errorf("откат поставки %s/%d: %w", dictionary, version, err)
	}
	if tag.RowsAffected() == 0 {
		return reference.ErrDeliveryConflict
	}
	return nil
}

// ReplacePresence заменяет строки присутствия записи целиком.
func (r *References) ReplacePresence(ctx context.Context, entry ids.UUID, rows []reference.Presence, at time.Time) error {
	if _, err := r.q.Exec(ctx, deletePresence, string(entry)); err != nil {
		return fmt.Errorf("присутствие %s: %w", entry, err)
	}
	if len(rows) == 0 {
		return nil
	}
	categories := make([]string, 0, len(rows))
	offers := make([]int32, 0, len(rows))
	sellers := make([]int32, 0, len(rows))
	for _, row := range rows {
		categories = append(categories, row.CategoryID)
		offers = append(offers, int32(row.Offers))   //nolint:gosec // счётчик предложений меньше int32
		sellers = append(sellers, int32(row.Sellers)) //nolint:gosec // счётчик продавцов меньше int32
	}
	_, err := r.q.Exec(ctx, insertPresence, string(entry), categories, offers, sellers, at)
	if isForeignKey(err) {
		return reference.ErrEntryNotFound
	}
	if err != nil {
		return fmt.Errorf("присутствие %s: %w", entry, err)
	}
	return nil
}
