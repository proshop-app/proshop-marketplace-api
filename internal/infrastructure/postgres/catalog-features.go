package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	selectBuyerInterests = `
		WITH interests AS (
			SELECT tenant_id, product_id, false AS purchased
			FROM buyer_favorites WHERE buyer_id = $1
			UNION
			SELECT l.tenant_id, l.product_id, false AS purchased
			FROM carts c JOIN cart_lines l ON l.cart_id = c.id
			WHERE c.buyer_id = $1 AND c.expires_at > now()
			UNION
			SELECT g.tenant_id, line->>'productId',
				(g.order_progress->>'Status' IN ('paid', 'shipped', 'delivered'))
			FROM checkouts c JOIN checkout_groups g ON g.checkout_id = c.id,
				jsonb_array_elements(g.lines) line
			WHERE c.buyer_id = $1
				AND g.order_progress->>'Status' IN (
					'paid', 'shipped', 'delivered', 'awaiting_payment', 'creating_order'
				)
		)
		SELECT DISTINCT COALESCE(p.category_id, ''),
			i.tenant_id || '~' || i.product_id, i.purchased
		FROM interests i LEFT JOIN catalog_products p
			ON p.tenant_id = i.tenant_id AND p.product_id = i.product_id`

	// Каждый магазин даёт не больше limit кандидатов до свёртки и оконного ранжирования.
	selectRecommendations = `
		WITH eligible AS MATERIALIZED (
			SELECT candidate.*
			FROM participants m
			JOIN tenants t ON t.tenant_id = m.tenant_id
			CROSS JOIN LATERAL (
				SELECT p.tenant_id, p.product_id, p.variant_group, p.sales_count,
					p.ratings_count, p.average_rating, p.updated_at,
					md5(p.tenant_id || '~' || p.product_id) AS offer_key
				FROM catalog_products p
				JOIN moderation_verdicts mv ON mv.tenant_id = p.tenant_id AND mv.product_id = p.product_id
				CROSS JOIN LATERAL (
					SELECT CASE WHEN $7::jsonb IS NULL OR ($4::bigint IS NULL AND $5::bigint IS NULL)
						THEN p.default_price ELSE
						(SELECT (profile->>'effectivePrice')::bigint
						 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(p.price_profiles) = 'array'
						 THEN p.price_profiles ELSE '[]'::jsonb END) profile
						 WHERE profile->>'key' = $7::jsonb->>p.tenant_id) END AS price
				) pricing
				WHERE p.tenant_id = m.tenant_id AND p.on_sale AND p.default_price IS NOT NULL
					AND mv.status = 'allowed' AND pricing.price IS NOT NULL
					AND NOT EXISTS (
						SELECT 1 FROM catalog_products excluded
						WHERE excluded.tenant_id = p.tenant_id AND excluded.variant_group = p.variant_group
							AND excluded.tenant_id || '~' || excluded.product_id = ANY($2::text[])
					)
					AND ($4::bigint IS NULL OR pricing.price >= $4)
					AND ($5::bigint IS NULL OR pricing.price <= $5)
					AND (cardinality($1::text[]) = 0 OR p.category_id = ANY($1::text[]))
				ORDER BY ` + recommendationRank + `
				LIMIT $8
			) candidate
			WHERE m.enabled AND m.delivery_valid AND t.status = 'active'
				AND ($3 = '' OR m.delivery_settings->'regions' ? $3)
		), candidates AS (
			SELECT DISTINCT ON (tenant_id, variant_group) *
			FROM eligible
			ORDER BY tenant_id, variant_group, ` + recommendationRank + `
		), ranked AS (
			SELECT *, row_number() OVER (PARTITION BY tenant_id ORDER BY ` + recommendationRank + `) AS tenant_rank
			FROM candidates
		), selected AS (
			SELECT *, row_number() OVER (ORDER BY
				CASE WHEN $6 = '' OR tenant_id <> $6 THEN 0 ELSE 1 END,
				GREATEST(tenant_rank - (($8::int + 3) / 4), 0), ` + recommendationRank + `) AS position
			FROM ranked
			ORDER BY position
			LIMIT $8
		)
		` + offerSelect + `
		JOIN selected s ON s.tenant_id = p.tenant_id AND s.product_id = p.product_id
		ORDER BY s.position`

	recommendationRank = `sales_count DESC, ratings_count DESC, average_rating DESC,
			updated_at DESC, offer_key, product_id`

	selectViewedCategories = `
		SELECT DISTINCT p.category_id
		FROM unnest($1::text[], $2::text[]) AS viewed(tenant_id, product_id)
		JOIN catalog_products p USING (tenant_id, product_id)
		JOIN participants pa ON pa.tenant_id = p.tenant_id
		JOIN tenants t ON t.tenant_id = p.tenant_id
		JOIN moderation_verdicts mv ON mv.tenant_id = p.tenant_id AND mv.product_id = p.product_id
		WHERE ` + visibleOfferSQL + `
			AND p.category_id IS NOT NULL AND p.category_id <> ''
		ORDER BY p.category_id`
)

// CatalogFeatures читает интересы и рекомендации из локальной проекции.
type CatalogFeatures struct {
	source *connectionSource
}

// NewCatalogFeatures подключает чтение без дополнительных вызовов на каждый товар.
func NewCatalogFeatures(pool *pgxpool.Pool) *CatalogFeatures {
	return &CatalogFeatures{source: newConnectionSource(pool)}
}

// Interests использует избранное, корзину и заказы, включая оформление.
func (r *CatalogFeatures) Interests(ctx context.Context, buyer ids.UUID) (ports.BuyerInterestSet, error) {
	result := ports.BuyerInterestSet{Categories: []string{}, Purchased: []ids.OfferID{}}
	err := r.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		rows, err := rawSQL(ctx, db, selectBuyerInterests, string(buyer)).Rows()
		if err != nil {
			return err
		}
		defer func() {
			_ = rows.Close()
		}()
		categories := map[string]bool{}
		for rows.Next() {
			var category, offer string
			var purchased bool
			if err := rows.Scan(&category, &offer, &purchased); err != nil {
				return err
			}
			if category != "" && !categories[category] {
				categories[category] = true
				result.Categories = append(result.Categories, category)
			}
			if purchased {
				id, err := ids.ParseOfferID(offer)
				if err != nil {
					return err
				}
				result.Purchased = append(result.Purchased, id)
			}
		}
		return rows.Err()
	})
	return result, err
}

// Recommend отбирает видимые предложения, цены и исключения до LIMIT.
func (r *CatalogFeatures) Recommend(
	ctx context.Context,
	filter ports.RecommendationFilter,
) ([]ports.OfferState, error) {
	categories := append([]string{}, filter.Categories...)
	excluded := make([]string, 0, len(filter.Excluded))
	for _, id := range filter.Excluded {
		excluded = append(excluded, id.String())
	}
	var contexts any
	if filter.PriceContexts != nil {
		raw, err := json.Marshal(filter.PriceContexts)
		if err != nil {
			return nil, err
		}
		contexts = string(raw)
	}
	args := []any{
		categories,
		excluded,
		filter.Region,
		filter.PriceMin,
		filter.PriceMax,
		string(filter.OtherTenant),
		contexts,
		filter.Limit,
	}
	var result []ports.OfferState
	err := r.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		reader := Offers{db: db}
		var err error
		result, err = reader.query(ctx, selectRecommendations, args...)
		return err
	})
	return result, err
}

// ViewedCategories читает категории видимых просмотров одним запросом по ключам проекции.
func (r *CatalogFeatures) ViewedCategories(ctx context.Context, viewed []ids.OfferID) ([]string, error) {
	tenants, products := make([]string, 0, len(viewed)), make([]string, 0, len(viewed))
	for _, id := range viewed {
		tenants = append(tenants, string(id.Tenant))
		products = append(products, string(id.Product))
	}
	var categories []string
	err := r.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		return rawSQL(ctx, db, selectViewedCategories, tenants, products).Scan(&categories).Error
	})
	return categories, err
}
