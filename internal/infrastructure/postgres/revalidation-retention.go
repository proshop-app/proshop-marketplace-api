package postgres

import (
	"context"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const purgeRevalidationFailures = `WITH page AS MATERIALIZED (
    SELECT tenant_id, product_id, source_revision, product_version, taxonomy_version, failed_at
    FROM taxonomy_revalidation_failures
    WHERE (tenant_id, product_id) > ($1, $2)
    ORDER BY tenant_id, product_id LIMIT $3
), removed AS (
    DELETE FROM taxonomy_revalidation_failures f USING page x
    WHERE f.tenant_id = x.tenant_id AND f.product_id = x.product_id
        AND f.source_revision = x.source_revision AND f.product_version = x.product_version
        AND f.taxonomy_version = x.taxonomy_version AND f.failed_at = x.failed_at
        AND NOT EXISTS (SELECT 1 FROM catalog_products p
            WHERE p.tenant_id = f.tenant_id AND p.product_id = f.product_id AND ` + activeRevalidationFailure + `)
    RETURNING 1
)
SELECT COALESCE((SELECT tenant_id FROM page ORDER BY tenant_id DESC, product_id DESC LIMIT 1), ''),
    COALESCE((SELECT product_id FROM page ORDER BY tenant_id DESC, product_id DESC LIMIT 1), ''),
    (SELECT count(*) FROM page), (SELECT count(*) FROM removed)`

// purgeRevalidationFailures идёт по PK только таблицы отказов, включая актуальные записи.
// Курсор не даёт повторно сканировать её начало при каждом удалении пачки.
// Текущая ошибка не истекает по времени: она нужна оператору до исправления карточки.
func (r *Retention) purgeRevalidationFailures(ctx context.Context, db *gorm.DB, batch int) (int64, error) {
	var total int64
	var cursor ports.OfferKey
	for {
		var scanned, removed int64
		err := rawSQL(ctx, db, purgeRevalidationFailures, cursor.TenantID, cursor.ProductID, batch).Row().Scan(
			&cursor.TenantID,
			&cursor.ProductID,
			&scanned,
			&removed,
		)
		if err != nil {
			return total, err
		}
		total += removed
		if scanned < int64(batch) {
			return total, nil
		}
	}
}
