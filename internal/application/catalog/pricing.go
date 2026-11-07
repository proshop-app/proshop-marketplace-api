package catalog

import (
	"context"
	"slices"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const priceContextBatchSize = 100

func (q *Queries) priceContexts(
	ctx context.Context,
	buyerID ids.UUID,
	tenantFilter string,
) (map[ids.TenantID]string, error) {
	var tenants []ids.TenantID
	if tenantFilter != "" {
		tenants = []ids.TenantID{ids.TenantID(tenantFilter)}
	}
	return PriceContexts(ctx, q.uow, q.platform, buyerID, tenants)
}

// PriceContexts выбирает контексты публичных магазинов из того же источника, что каталог.
// nil-фильтр выбирает все магазины, пустой список — ни одного.
func PriceContexts(
	ctx context.Context,
	uow ports.UnitOfWork,
	pricing ports.CatalogPricing,
	buyerID ids.UUID,
	tenantFilter []ids.TenantID,
) (map[ids.TenantID]string, error) {
	if buyerID == "" {
		return nil, nil
	}
	tenants := []ids.TenantID{}
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for offset := 0; ; offset += priceContextBatchSize {
			list, _, err := tx.Participants().List(ctx, participant.Page{
				Limit:  priceContextBatchSize,
				Offset: offset,
			})
			if err != nil {
				return err
			}
			for _, seller := range list {
				state := seller.State()
				if !state.Enabled || (tenantFilter != nil && !slices.Contains(tenantFilter, state.TenantID)) {
					continue
				}
				tenant, err := tx.Registry().Get(ctx, state.TenantID)
				if err != nil {
					return err
				}
				if participant.VisibilityOf(seller, tenant).Visible {
					tenants = append(tenants, state.TenantID)
				}
			}
			if len(list) < priceContextBatchSize {
				return nil
			}
		}
	})
	if err != nil {
		return nil, err
	}
	contexts := make(map[ids.TenantID]string, len(tenants))
	for start := 0; start < len(tenants); start += priceContextBatchSize {
		end := min(start+priceContextBatchSize, len(tenants))
		batch, err := pricing.PriceContexts(ctx, buyerID, tenants[start:end])
		if err != nil {
			return nil, err
		}
		for tenant, key := range batch {
			contexts[tenant] = key
		}
	}
	return contexts, nil
}
