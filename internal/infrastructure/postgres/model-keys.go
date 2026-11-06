package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	publishedModelKeys = `
        SELECT p.tenant_id, p.product_id, p.model_key,
            COALESCE(p.model_key_source, 'none') AS source,
            COALESCE(p.card->'publication'->'categoryPath'->>0, '') AS direction,
            p.card
        FROM catalog_products p
        JOIN participants pa ON pa.tenant_id = p.tenant_id
        JOIN tenants t ON t.tenant_id = p.tenant_id
        WHERE NOT p.deleted AND pa.stage = 'connected' AND pa.delivery_valid AND t.status = 'active'
            AND p.card->>'visible' = 'true'
            AND p.card->'publication'->>'published' = 'true'
            AND pa.subsidy->>'enabled' = 'true'
            AND jsonb_array_length(pa.delivery_settings->'regions') > 0
            AND jsonb_array_length(pa.delivery_settings->'carriers') > 0
            AND EXISTS (
                SELECT 1 FROM jsonb_array_elements(p.price_profiles) profile
                WHERE profile->>'key' = 'marketplace:default'
            )`
	modelKeyCounts = `WITH published AS (` + publishedModelKeys + `),
        grouped AS (
            SELECT direction, source, model_key, COUNT(*) AS offers,
                COUNT(DISTINCT tenant_id) AS sellers
            FROM published
            GROUP BY GROUPING SETS ((direction, source, model_key), (source, model_key))
        )
        SELECT COALESCE(direction, ''), source, SUM(offers),
            COUNT(*) FILTER (WHERE model_key IS NOT NULL AND sellers >= 2)
        FROM grouped
        GROUP BY direction, source
        ORDER BY direction NULLS FIRST, source`
	modelKeySamples = `WITH published AS (` + publishedModelKeys + `)
        SELECT model_key, source,
            jsonb_agg(jsonb_build_object(
                'tenantId', tenant_id, 'productId', product_id,
                'name', card->>'name',
                'brand', card->'publication'->'attributes'->'brand',
                'part_number', card->'publication'->'attributes'->'part_number',
                'gtin', card->'publication'->'attributes'->'gtin'
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
	pool *pgxpool.Pool
}

// NewModelKeyStats создаёт источник статистики ключей модели.
func NewModelKeyStats(pool *pgxpool.Pool) *ModelKeyStats {
	return &ModelKeyStats{
		pool: pool,
	}
}

// Report собирает покрытие и, по запросу, примеры совпадений.
func (s *ModelKeyStats) Report(ctx context.Context, samples bool) (ports.ModelKeyReport, error) {
	report := ports.ModelKeyReport{
		Counts:  []ports.ModelKeyCount{},
		Samples: []ports.ModelKeySample{},
	}
	rows, err := s.pool.Query(ctx, modelKeyCounts)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	for rows.Next() {
		var count ports.ModelKeyCount
		if err := rows.Scan(&count.Direction, &count.Source, &count.Offers, &count.Shared); err != nil {
			return report, err
		}
		report.Counts = append(report.Counts, count)
	}
	totals := make(map[string]int64)
	for _, count := range report.Counts {
		totals[count.Direction] += count.Offers
	}
	for i := range report.Counts {
		count := &report.Counts[i]
		if total := totals[count.Direction]; total > 0 {
			count.Share = float64(count.Offers) / float64(total)
		}
	}
	if err := rows.Err(); err != nil || !samples {
		return report, err
	}
	rows.Close()
	records, err := s.pool.Query(ctx, modelKeySamples)
	if err != nil {
		return report, err
	}
	defer records.Close()
	for records.Next() {
		var sample ports.ModelKeySample
		if err := records.Scan(&sample.ModelKey, &sample.Source, &sample.Offers); err != nil {
			return report, err
		}
		report.Samples = append(report.Samples, sample)
	}

	return report, records.Err()
}
