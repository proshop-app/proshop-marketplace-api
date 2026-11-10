package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	purgePromotionViews = `DELETE FROM promotion_views WHERE ctid IN
 (SELECT v.ctid FROM promotion_views v JOIN promotion_campaigns c ON c.id=v.campaign_id
 WHERE c.date_to < $1::date LIMIT $2)`

	purgeProcessedEvents = `
		DELETE FROM processed_events
		WHERE id IN (SELECT id FROM processed_events WHERE processed_at < $1 LIMIT $2)`

	purgeDoneTasks = `
		DELETE FROM outbox
		WHERE id IN (SELECT id FROM outbox WHERE done_at < $1 LIMIT $2)`

	purgeParkedEvents = `
		DELETE FROM parked_events
		WHERE id IN (SELECT id FROM parked_events WHERE parked_at < $1 AND status <> 'queued' LIMIT $2)`

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
func NewRetention(pool *pgxpool.Pool) *Retention {
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
		{sql: purgeProcessedEvents, before: request.ProcessedBefore, count: &result.Processed},
		{sql: purgeDoneTasks, before: request.OutboxDoneBefore, count: &result.OutboxDone},
		{sql: purgeParkedEvents, before: request.ParkedBefore, count: &result.Parked},
		{sql: purgeChallenges, before: request.ChallengesBefore, count: &result.Challenges},
		{sql: purgeCarts, before: request.CartsBefore, count: &result.Carts},
		{sql: purgePromotionViews, before: request.PromotionBefore, count: &result.PromotionViews},
	}

	err = r.source.withConnection(ctx, func(db *gorm.DB) error {
		for _, step := range steps {
			if *step.count, err = r.purge(ctx, db, step.sql, step.before, request.Batch); err != nil {
				return err
			}
		}

		return nil
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
