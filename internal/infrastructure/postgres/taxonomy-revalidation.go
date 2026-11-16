package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

const (
	queueRevalidation = `INSERT INTO taxonomy_revalidation (version, category)
		SELECT $1, unnest($2::text[])`
	nextRevalidation = `SELECT version, category, tenant_cursor, product_cursor, processed, done
		FROM taxonomy_revalidation WHERE NOT done
		ORDER BY version, category LIMIT 1 FOR UPDATE SKIP LOCKED`
	advanceRevalidation = `UPDATE taxonomy_revalidation
		SET tenant_cursor = $3,
			product_cursor = $4,
			processed = $5,
			done = $6
		WHERE version = $1 AND category = $2`
	categoryProducts = `SELECT ` + productColumns + ` FROM catalog_products p
		WHERE card->'marketplace'->>'categoryId' = $1
		AND (tenant_id, product_id) > ($2, $3)
		ORDER BY tenant_id, product_id LIMIT $4 FOR UPDATE`
)

// QueueRevalidation не читает товары: одна запись пачки категорий в транзакции публикации.
func (r *Taxonomy) QueueRevalidation(ctx context.Context, version int64, categories []string) error {
	_, err := execSQL(ctx, r.db, queueRevalidation, version, categories)
	return err
}

// NextRevalidation удерживает только выбранное задание до фиксации страницы.
func (r *Taxonomy) NextRevalidation(ctx context.Context) (*taxonomy.Revalidation, error) {
	var job taxonomy.Revalidation
	err := rawSQL(ctx, r.db, nextRevalidation).Row().Scan(
		&job.Version,
		&job.Category,
		&job.TenantCursor,
		&job.ProductCursor,
		&job.Processed,
		&job.Done,
	)
	if notFound(err) {
		return nil, nil
	}
	return &job, err
}

// AdvanceRevalidation вызывается под блокировкой NextRevalidation.
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
	)
	return err
}

// RevalidationPage использует индекс исходной категории и удерживает строки от гонки с событиями.
func (r *Products) RevalidationPage(
	ctx context.Context,
	job taxonomy.Revalidation,
	limit int,
) ([]*catalog.Product, error) {
	rows, err := rawSQL(
		ctx,
		r.db,
		categoryProducts,
		job.Category,
		job.TenantCursor,
		job.ProductCursor,
		limit,
	).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []*catalog.Product{}
	for rows.Next() {
		var row productRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, err
		}
		product, err := row.product()
		if err != nil {
			return nil, err
		}
		items = append(items, product)
	}
	return items, rows.Err()
}
