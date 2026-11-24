package postgres

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	purgeBulkApprovals = `DELETE FROM moderation_bulk_idempotency WHERE key_hash IN
        (SELECT key_hash FROM moderation_bulk_idempotency WHERE expires_at < $1 ORDER BY expires_at LIMIT $2)`
	purgePromotionViews = `DELETE FROM promotion_views WHERE ctid IN
 (SELECT v.ctid FROM promotion_views v JOIN promotion_campaigns c ON c.id=v.campaign_id
 WHERE c.date_to < $1::date LIMIT $2)`

	purgeProcessedEvents = `
		DELETE FROM processed_events
		WHERE id IN (SELECT id FROM processed_events WHERE processed_at < $1 LIMIT $2)`

	// Выполненные задачи удаляются при завершении; по сроку уходит только карантин покупок поиска.
	purgePurchaseQuarantine = `
		DELETE FROM outbox
		WHERE id IN (
			SELECT id FROM outbox
			WHERE kind = 'search.purchase' AND failed_at < $1
			ORDER BY failed_at, id
			LIMIT $2
		)`

	purgeParkedEvents = `
		DELETE FROM parked_events
		WHERE id IN (SELECT id FROM parked_events WHERE status = 'resolved' AND updated_at < $1 LIMIT $2)`

	purgeChallenges = `
		DELETE FROM phone_challenges
		WHERE id IN (SELECT id FROM phone_challenges WHERE expires_at < $1 LIMIT $2)`

	purgeCarts = `
		DELETE FROM carts
		WHERE id IN (SELECT id FROM carts WHERE expires_at < $1 LIMIT $2)`
)

// Retention — чистка служебных записей пачками, чтобы не держать долгих блокировок.
type Retention struct {
	source *connectionSource
}

// NewRetention — чистка поверх пула.
func NewRetention(pool *ClassPool) *Retention {
	return &Retention{source: newConnectionSource(pool)}
}

// Purge удаляет записи старше границ.
func (r *Retention) Purge(ctx context.Context, request ports.PurgeRequest) (ports.PurgeResult, error) {
	var (
		result ports.PurgeResult
		err    error
	)

	steps := []struct {
		sql    string
		before time.Time
		count  *int64
	}{
		{sql: purgeBulkApprovals, before: request.BulkBefore, count: &result.BulkApprovals},
		{sql: purgeProcessedEvents, before: request.ProcessedBefore, count: &result.Processed},
		{sql: purgePurchaseQuarantine, before: request.PurchaseQuarantineBefore, count: &result.PurchaseQuarantine},
		{sql: purgeParkedEvents, before: request.ParkedBefore, count: &result.Parked},
		{sql: purgeChallenges, before: request.ChallengesBefore, count: &result.Challenges},
		{sql: purgeCarts, before: request.CartsBefore, count: &result.Carts},
		{sql: purgePromotionViews, before: request.PromotionBefore, count: &result.PromotionViews},
	}

	err = r.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		for _, step := range steps {
			if *step.count, err = r.purge(ctx, db, step.sql, step.before, request.Batch); err != nil {
				return err
			}
		}

		result.RevalidationFailures, err = r.purgeRevalidationFailures(ctx, db, request.Batch)
		return err
	})

	return result, err
}

func (r *Retention) purge(ctx context.Context, db *gorm.DB, sql string, before time.Time, batch int) (int64, error) {
	var total int64

	for {
		affected, err := execSQL(ctx, db, sql, before, batch)
		if err != nil {
			return total, fmt.Errorf("чистка: %w", err)
		}

		total += affected
		if affected < int64(batch) {
			return total, nil
		}
	}
}
