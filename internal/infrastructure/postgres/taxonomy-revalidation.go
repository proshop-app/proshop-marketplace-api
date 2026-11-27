package postgres

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

const (
	queueRevalidation = `WITH superseded AS (
        UPDATE taxonomy_revalidation SET done = true
        WHERE NOT done AND version < $1 AND category = ANY($2::text[])
    ) INSERT INTO taxonomy_revalidation (version, category) SELECT $1, unnest($2::text[])`
	nextRevalidation = `WITH candidate AS (
        SELECT version, category FROM taxonomy_revalidation
        WHERE NOT done AND available_at <= now()
        ORDER BY available_at, version, category LIMIT 1 FOR UPDATE SKIP LOCKED
    ) UPDATE taxonomy_revalidation j
        SET available_at = now() + interval '1 second',
            lease = j.lease + 1
        FROM candidate c WHERE j.version = c.version AND j.category = c.category
        RETURNING j.version, j.category, tenant_cursor, product_cursor, processed, done, lease, available_at`
	lockRevalidation = `SELECT version, category, tenant_cursor, product_cursor, processed, done, lease, available_at
        FROM taxonomy_revalidation WHERE version = $1 AND category = $2 AND lease = $3 AND NOT done FOR UPDATE`
	advanceRevalidation = `UPDATE taxonomy_revalidation
		SET tenant_cursor = $3,
			product_cursor = $4,
			processed = $5,
			done = $6,
			available_at = now()
		WHERE version = $1 AND category = $2 AND lease = $7`
	categoryProducts = `SELECT ` + productColumns + ` FROM catalog_products p
		WHERE card->'marketplace'->>'categoryId' = $1
		AND (tenant_id, product_id) > ($2, $3)
		ORDER BY tenant_id, product_id LIMIT $4 FOR UPDATE`
)

// QueueRevalidation не читает товары: одна запись пачки категорий в транзакции публикации.
func (r *Taxonomy) QueueRevalidation(ctx context.Context, version int64, categories []string) error {
	affected, err := execSQL(ctx, r.db, queueRevalidation, version, categories)
	if err == nil && affected > 0 && r.wake != nil {
		r.wake(ports.TaxonomyWork, time.Time{})
	}
	return err
}

// NextRevalidation выдаёт короткую аренду, которая фиксируется до начала страницы.
func (r *Taxonomy) NextRevalidation(ctx context.Context) (*taxonomy.Revalidation, error) {
	return r.revalidation(ctx, nextRevalidation)
}

// LockRevalidation проверяет аренду после общей блокировки головы и пропускает закрытое задание.
func (r *Taxonomy) LockRevalidation(ctx context.Context, job taxonomy.Revalidation) (*taxonomy.Revalidation, error) {
	return r.revalidation(ctx, lockRevalidation, job.Version, job.Category, job.Lease)
}

func (r *Taxonomy) revalidation(ctx context.Context, query string, args ...any) (*taxonomy.Revalidation, error) {
	var job taxonomy.Revalidation
	err := rawSQL(ctx, r.db, query, args...).Row().Scan(
		&job.Version,
		&job.Category,
		&job.TenantCursor,
		&job.ProductCursor,
		&job.Processed,
		&job.Done,
		&job.Lease,
		&job.AvailableAt,
	)
	if notFound(err) {
		return nil, nil
	}
	return &job, err
}

// AdvanceRevalidation вызывается под блокировкой LockRevalidation.
func (r *Taxonomy) AdvanceRevalidation(ctx context.Context, job taxonomy.Revalidation) error {
	_, err := execSQL(
		ctx,
		r.db,
		advanceRevalidation,
		job.Version,
		job.Category,
		job.TenantCursor,
		job.ProductCursor,
		job.Processed,
		job.Done,
		job.Lease,
	)
	return err
}
