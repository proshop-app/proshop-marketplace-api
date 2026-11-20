package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

const (
	// Вычисляемые колонки повторяют ComposeOffer; остаток не ограничивает видимость.
	visibleOfferSQL = `p.on_sale AND p.price IS NOT NULL
        AND pa.enabled AND pa.delivery_valid AND t.status = 'active' AND mv.status = 'allowed'`
	selectSellersCounts = `SELECT p.model_key, COUNT(DISTINCT p.tenant_id)
        FROM catalog_products p
        JOIN participants pa ON pa.tenant_id = p.tenant_id
        JOIN tenants t ON t.tenant_id = p.tenant_id
        JOIN moderation_verdicts mv ON mv.tenant_id = p.tenant_id AND mv.product_id = p.product_id
        WHERE p.model_key IS NOT NULL AND p.model_key = ANY($1::text[])
            AND ` + visibleOfferSQL + `
        GROUP BY p.model_key`
)

// SellersCounts читает актуальную видимость отдельно от запаздывающего поискового индекса.
type SellersCounts struct {
	source *connectionSource
}

// NewSellersCounts подключает подсчёт вне транзакции основной выдачи.
func NewSellersCounts(pool *pgxpool.Pool) *SellersCounts {
	return &SellersCounts{source: newConnectionSource(pool)}
}

// CountSellers не занимает соединение для страницы без известных ключей.
func (s *SellersCounts) CountSellers(ctx context.Context, keys []string) (map[string]int, error) {
	counts := make(map[string]int)
	if len(keys) == 0 {
		return counts, nil
	}
	err := s.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		rows, err := rawSQL(ctx, db, selectSellersCounts, keys).Rows()
		if err != nil {
			return err
		}
		defer func() {
			_ = rows.Close()
		}()
		for rows.Next() {
			var key string
			var count int
			if err := rows.Scan(&key, &count); err != nil {
				return err
			}
			counts[key] = count
		}
		return rows.Err()
	})
	return counts, err
}
