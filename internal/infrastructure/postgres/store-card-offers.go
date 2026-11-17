package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const storeRatingGroup = `CASE WHEN COALESCE(p.card->>'variantsId', '') = ''
    THEN 'product:' || p.product_id ELSE 'variants:' || (p.card->>'variantsId') END`

const storeSummaryBatchSize = 200

const declareStoreSummary = `DECLARE store_summary NO SCROLL CURSOR FOR ` + offerSelect + `
 WHERE p.tenant_id = $1 AND NOT p.deleted
 AND (COALESCE(p.card->'marketplace'->>'published', 'false')::boolean
 OR COALESCE(p.card->'publication'->>'published', 'false')::boolean)
 ORDER BY (` + storeRatingGroup + `) COLLATE "C", p.product_id COLLATE "C"`

const fetchStoreSummary = `FETCH FORWARD 200 FROM store_summary`

const closeStoreSummary = `CLOSE store_summary`

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

// WalkStoreSummaryOffers сохраняет снимок курсора при смене группы вариантов между страницами.
// READ COMMITTED остаётся у транзакции: свежая постмодерация карточки сохраняется через CAS проекции.
func (r *Offers) WalkStoreSummaryOffers(
	ctx context.Context,
	tenant ids.TenantID,
	visit func([]ports.OfferState) error,
) (err error) {
	if _, err := execSQL(ctx, r.db, declareStoreSummary, string(tenant)); err != nil {
		return err
	}
	defer func() {
		_, closeErr := execSQL(ctx, r.db, closeStoreSummary)
		if err == nil {
			err = closeErr
		}
	}()
	for {
		states, err := r.query(ctx, fetchStoreSummary)
		if err != nil {
			return err
		}
		if len(states) > 0 {
			if err := visit(states); err != nil {
				return err
			}
		}
		if len(states) < storeSummaryBatchSize {
			return nil
		}
	}
}
