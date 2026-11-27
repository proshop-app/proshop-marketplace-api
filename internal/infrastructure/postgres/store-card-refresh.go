package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	storeSummaryPageSize = 200

	// Параллельная индексация может встретить тот же ключ. Пропущенные здесь
	// строки уже взяты другим индексатором или самой сводкой.
	scheduleStoreSummaryChanges = `
		WITH picked AS (
			SELECT s.id
			FROM unnest($1::text[], $2::text[]) k(tenant_id, product_id)
			JOIN store_summary_changes s USING (tenant_id, product_id)
			WHERE s.batch_no IS NOT NULL AND NOT s.scheduled
			FOR UPDATE OF s SKIP LOCKED
		), moved AS (
			UPDATE store_summary_changes s SET scheduled = true FROM picked WHERE s.id = picked.id
			RETURNING s.tenant_id
		)
		INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
		SELECT DISTINCT $3::text, tenant_id, $3::text || ':' || tenant_id, '{}'::jsonb, $4::timestamptz, $4::timestamptz
		FROM moved ORDER BY tenant_id
		` + mergePendingTasks

	applyStoreSummaryPage = `SELECT apply_store_summary_changes($1, $2)`

	readStoreSummaryFacts = `
		WITH counts AS MATERIALIZED (
			SELECT kind, category, total FROM store_summary_counts
			WHERE tenant_id = $1 AND category = ANY($2::text[])
		), totals AS (
			SELECT COALESCE(max(total) FILTER (WHERE kind = 'candidate_total'), 0) AS candidates,
				CASE WHEN $3 THEN COALESCE(max(total) FILTER (WHERE kind = 'visible_total'), 0) ELSE 0 END AS visible,
				COALESCE(max(total) FILTER (WHERE kind = 'raw_total'), 0)
				 - COALESCE(sum(total) FILTER (WHERE kind = 'raw' AND category <> ''), 0) AS uncategorized
			FROM counts
		)
		SELECT CASE WHEN t.visible > 0 THEN t.visible ELSE t.candidates END, t.visible, t.uncategorized,
			CASE WHEN t.visible > 0 THEN (
				SELECT price FROM store_summary_products WHERE tenant_id = $1 AND visible ORDER BY price LIMIT 1
			) ELSE (
				SELECT price FROM store_summary_products
				WHERE tenant_id = $1 AND price IS NOT NULL ORDER BY price LIMIT 1
			) END,
			COALESCE((SELECT jsonb_object_agg(category, total) FROM counts
				WHERE category <> '' AND kind = CASE WHEN t.visible > 0 THEN 'visible' ELSE 'candidate' END), '{}'),
			COALESCE((SELECT jsonb_agg(row) FROM (
				SELECT product_id AS "productId", ($1 || '~' || product_id) AS "offerId",
					name, NULLIF(image, '') AS image
				FROM store_summary_products WHERE tenant_id = $1 AND visible AND t.visible > 0
				ORDER BY sales DESC, (image <> '') DESC, ranked_at DESC, ranked_revision DESC, product_id LIMIT 3
			) row), '[]'),
			CASE WHEN t.visible > 0 THEN COALESCE(r.ratings, 0) ELSE 0 END,
			COALESCE((r.weight / NULLIF(r.ratings, 0))::float8, 0),
			CASE WHEN t.visible > 0 THEN COALESCE(r.card_version, 0)::text ELSE '' END
		FROM totals t LEFT JOIN store_summary_ratings r ON r.tenant_id = $1`
)

// ScheduleChanges отмечает обработанные при индексации дельты и будит сводку.
func (r *StoreCards) ScheduleChanges(ctx context.Context, keys []ids.OfferID, at time.Time) error {
	tenants, products := offerKeyColumns(keys)
	affected, err := execSQL(ctx, r.db, scheduleStoreSummaryChanges, tenants, products, ports.TaskStoreCard, at)
	if err == nil && affected > 0 && r.wake != nil {
		r.wake(ports.TaskStoreCard, at)
	}
	return err
}

// Rollup читает только порцию изменившихся товаров, счётчики и первые строки индексов.
func (r *StoreCards) Rollup(
	ctx context.Context,
	tenant ids.TenantID,
	visible bool,
	categories []string,
) (ports.StoreSummaryFacts, error) {
	var result ports.StoreSummaryFacts
	var consumed int
	if err := rawSQL(ctx, r.db, applyStoreSummaryPage, string(tenant), storeSummaryPageSize).
		Row().Scan(&consumed); err != nil {
		return result, err
	}
	if consumed == storeSummaryPageSize {
		result.More = true
		return result, nil
	}
	var counts, products []byte
	var ratings int64
	var average float64
	categories = append(categories, "") // Общие счётчики отмечены пустым ключом.
	err := rawSQL(ctx, r.db, readStoreSummaryFacts, string(tenant), categories, visible).Row().Scan(
		&result.Offers,
		&result.VisibleOffers,
		&result.Uncategorized,
		&result.FromPrice,
		&counts,
		&products,
		&ratings,
		&average,
		&result.CacheSignature,
	)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(counts, &result.Categories); err != nil {
		return result, err
	}
	if err := json.Unmarshal(products, &result.Products); err != nil {
		return result, err
	}
	result.Rating = catalog.ProductRating(average, ratings)
	return result, nil
}
