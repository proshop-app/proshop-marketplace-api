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
	recommendationCandidateMultiplier = 10
	selectBuyerInterests              = `
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

	recommendationStart = `
		WITH top_candidates AS MATERIALIZED (
			SELECT p.tenant_id, p.product_id, p.variant_group, p.sales_count
			FROM catalog_products p
			JOIN participants pa ON pa.tenant_id = p.tenant_id
			JOIN tenants t ON t.tenant_id = p.tenant_id
			JOIN moderation_verdicts mv ON mv.tenant_id = p.tenant_id AND mv.product_id = p.product_id
			CROSS JOIN LATERAL (
				SELECT CASE WHEN $7::jsonb IS NULL OR ($4::bigint IS NULL AND $5::bigint IS NULL)
					THEN p.default_price ELSE
					(SELECT (profile->>'effectivePrice')::bigint
					 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(p.price_profiles) = 'array'
					 THEN p.price_profiles ELSE '[]'::jsonb END) profile
					 WHERE profile->>'key' = $7::jsonb->>p.tenant_id) END AS price
			) pricing
			WHERE p.on_sale AND pa.enabled AND pa.delivery_valid AND t.status = 'active'
				AND mv.status = 'allowed' AND pricing.price IS NOT NULL
				AND p.default_price IS NOT NULL
				AND NOT EXISTS (
					SELECT 1 FROM catalog_products excluded
					WHERE excluded.tenant_id = p.tenant_id AND excluded.variant_group = p.variant_group
						AND excluded.tenant_id || '~' || excluded.product_id = ANY($2::text[])
				)
				AND ($3 = '' OR pa.delivery_settings->'regions' ? $3)
				AND ($4::bigint IS NULL OR pricing.price >= $4)
				AND ($5::bigint IS NULL OR pricing.price <= $5)`

	selectRecommendationsByCategory = recommendationStart + `
				AND p.category_id = ANY($1::text[])` + recommendationOrder + `$9` + recommendationEnd
	selectRecommendationsPopular = recommendationStart + recommendationOrder + `$1` + recommendationEnd

	recommendationOrder = `
			ORDER BY p.sales_count DESC, p.tenant_id, p.product_id
			LIMIT `

	recommendationEnd = `
		), candidates AS (
			SELECT DISTINCT ON (tenant_id, variant_group) tenant_id, product_id, sales_count
			FROM top_candidates
			ORDER BY tenant_id, variant_group, sales_count DESC, product_id
		), selected AS (
			SELECT * FROM candidates
			ORDER BY CASE WHEN $6 = '' OR tenant_id <> $6 THEN 0 ELSE 1 END,
				sales_count DESC, tenant_id, product_id
			LIMIT $8
		)
		` + offerSelect + `
		JOIN selected s ON s.tenant_id = p.tenant_id AND s.product_id = p.product_id
		ORDER BY CASE WHEN $6 = '' OR p.tenant_id <> $6 THEN 0 ELSE 1 END,
			p.sales_count DESC, p.tenant_id, p.product_id`
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
	err := r.source.withConnection(ctx, func(db *gorm.DB) error {
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
	query := selectRecommendationsPopular
	args := []any{
		filter.Limit * recommendationCandidateMultiplier,
		excluded,
		filter.Region,
		filter.PriceMin,
		filter.PriceMax,
		string(filter.OtherTenant),
		contexts,
		filter.Limit,
	}
	if len(categories) > 0 {
		query = selectRecommendationsByCategory
		args[0] = categories
		args = append(args, filter.Limit*recommendationCandidateMultiplier)
	}
	var result []ports.OfferState
	err := r.source.withConnection(ctx, func(db *gorm.DB) error {
		reader := Offers{db: db}
		var err error
		result, err = reader.query(ctx, query, args...)
		return err
	})
	return result, err
}
