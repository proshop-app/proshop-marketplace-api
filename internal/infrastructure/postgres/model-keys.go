package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	publishedModelKeys = `
        SELECT p.tenant_id, p.product_id, p.model_key,
            COALESCE(p.model_key_source, 'none') AS source,
            COALESCE(p.card->'publication'->'categoryPath'->>0, '') AS direction,
            COALESCE(p.card->'publication'->>'schema', '') AS schema,
            EXISTS (
                SELECT 1 FROM jsonb_array_elements(
                    COALESCE(NULLIF(p.card->'publication'->'issues', 'null'), '[]')
                ) issue
                WHERE issue->>'code' = 'identity_incomplete'
            ) AS incomplete,
            p.card
        FROM catalog_products p
        JOIN participants pa ON pa.tenant_id = p.tenant_id
        JOIN tenants t ON t.tenant_id = p.tenant_id
        WHERE NOT p.deleted AND pa.stage = 'connected' AND pa.delivery_valid AND t.status = 'active'
            AND p.card->>'visible' = 'true'
            AND p.card->'publication'->>'published' = 'true'
            AND jsonb_array_length(pa.delivery_settings->'regions') > 0
            AND jsonb_array_length(pa.delivery_settings->'carriers') > 0
            AND p.price IS NOT NULL`
	modelKeyCounts = `WITH published AS (` + publishedModelKeys + `),
        grouped AS (
            SELECT direction, schema, source, model_key, COUNT(*) AS offers,
                COUNT(*) FILTER (WHERE incomplete) AS incomplete,
                COUNT(DISTINCT tenant_id) AS sellers
            FROM published
            GROUP BY GROUPING SETS (
                (direction, source, model_key), (schema, source, model_key), (source, model_key)
            )
            HAVING (GROUPING(schema) = 1 OR schema <> '')
                AND (GROUPING(direction) = 1 OR direction <> '')
        )
        SELECT COALESCE(direction, ''), COALESCE(schema, ''), source, SUM(offers),
            COUNT(*) FILTER (WHERE model_key IS NOT NULL AND sellers >= 2), SUM(incomplete)
        FROM grouped
        GROUP BY direction, schema, source
        ORDER BY direction NULLS FIRST, schema NULLS FIRST, source`
	modelKeySamples = `WITH published AS (` + publishedModelKeys + `)
        SELECT model_key, source,
            jsonb_agg(jsonb_build_object(
                'tenantId', tenant_id, 'productId', product_id,
                'name', card->>'name',
                'schema', schema, 'attributes', card->'publication'->'attributes',
                'variantKey', card->>'variantKey'
            ) ORDER BY tenant_id, product_id)
        FROM published
        WHERE model_key IS NOT NULL
        GROUP BY model_key, source
        HAVING COUNT(DISTINCT tenant_id) >= 2
        ORDER BY model_key
        LIMIT 200`
)

// ModelKeyStats предоставляет статистику опубликованных ключей модели.
type ModelKeyStats struct {
	source *connectionSource
}

// NewModelKeyStats создаёт источник статистики ключей модели.
func NewModelKeyStats(pool *pgxpool.Pool) *ModelKeyStats {
	return &ModelKeyStats{
		source: newConnectionSource(pool),
	}
}

// Report собирает покрытие и, по запросу, примеры совпадений.
func (s *ModelKeyStats) Report(ctx context.Context, samples bool) (ports.ModelKeyReport, error) {
	report := ports.ModelKeyReport{
		Counts:  []ports.ModelKeyCount{},
		Samples: []ports.ModelKeySample{},
	}

	err := s.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		return loadModelKeyReport(ctx, db, samples, &report)
	})

	return report, err
}

func loadModelKeyReport(ctx context.Context, db *gorm.DB, samples bool, report *ports.ModelKeyReport) error {
	rows, err := rawSQL(ctx, db, modelKeyCounts).Rows()
	if err != nil {
		return err
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var count ports.ModelKeyCount
		if err := rows.Scan(
			&count.Direction,
			&count.Schema,
			&count.Source,
			&count.Offers,
			&count.Shared,
			&count.IdentityIncomplete,
		); err != nil {
			return err
		}
		report.Counts = append(report.Counts, count)
	}
	totals := make(map[string]int64)
	for _, count := range report.Counts {
		totals[count.Direction+"\x00"+count.Schema] += count.Offers
	}
	for i := range report.Counts {
		count := &report.Counts[i]
		if total := totals[count.Direction+"\x00"+count.Schema]; total > 0 {
			count.Share = float64(count.Offers) / float64(total)
			count.IdentityIncompleteShare = float64(count.IdentityIncomplete) / float64(total)
		}
	}
	if err := rows.Err(); err != nil || !samples {
		return err
	}
	_ = rows.Close()
	records, err := rawSQL(ctx, db, modelKeySamples).Rows()
	if err != nil {
		return err
	}
	defer func() {
		_ = records.Close()
	}()
	for records.Next() {
		var sample ports.ModelKeySample
		if err := records.Scan(&sample.ModelKey, &sample.Source, &sample.Offers); err != nil {
			return err
		}
		report.Samples = append(report.Samples, sample)
	}

	return records.Err()
}
