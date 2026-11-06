package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const selectStoreCardOffers = offerSelect + `
 WHERE p.tenant_id = $1 AND p.product_id > $2 AND NOT p.deleted
 AND (COALESCE(p.card->'marketplace'->>'published', 'false')::boolean
 OR COALESCE(p.card->'publication'->>'published', 'false')::boolean)
 ORDER BY p.product_id LIMIT $3`

// StoreCardOffers отбирает публикации магазина для пересчёта сводки.
func (r *Offers) StoreCardOffers(
	ctx context.Context,
	tenant ids.TenantID,
	after ids.ProductID,
	limit int,
) ([]ports.OfferState, error) {
	return r.query(ctx, selectStoreCardOffers, string(tenant), string(after), limit)
}
