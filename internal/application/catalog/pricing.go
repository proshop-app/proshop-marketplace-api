package catalog

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const priceContextBatchSize = 100

type batchedPricing struct {
	pricing ports.CatalogPricing
	known   map[ids.TenantID]string
}

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

func priceOffer(
	doc *ports.OfferDocument,
	key string,
	publicState ports.OfferState,
) error {
	// На холодном отказе персонализации остаётся публичная карточка из проекции.
	// Оформление независимо перепроверит цену и наличие у продавца.
	if key == "" {
		// Пустой контекст возможен и для скрытого магазина: устаревший индекс не должен его раскрыть.
		public, ok := Document(publicState)
		if !ok || !public.Visible {
			return catalog.ErrOfferNotFound
		}
		*doc = public
		key = catalog.DefaultPriceProfile
	}
	if !selectOfferPrice(doc, key) {
		return catalog.ErrOfferNotFound
	}
	return nil
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
	if tenantFilter != nil && len(tenantFilter) == 0 {
		return map[ids.TenantID]string{}, nil
	}
	cached, isCached := pricing.(ports.CachedCatalogPricing)
	var profiles map[ids.TenantID]string
	if isCached {
		var err error
		profiles, err = cached.PriceContexts(ctx, buyerID, tenantFilter)
		if err != nil {
			return nil, err
		}
	}
	tenants, err := publicPriceTenants(ctx, uow, tenantFilter)
	if err != nil {
		return nil, err
	}
	if !isCached {
		return pricing.PriceContexts(ctx, buyerID, tenants)
	}
	result := make(map[ids.TenantID]string, len(tenants))
	complete := true
	for _, tenant := range tenants {
		result[tenant] = profiles[tenant]
		complete = complete && profiles[tenant] != ""
	}
	if !complete {
		cached.Preload(buyerID)
		for tenant := range result {
			result[tenant] = catalog.DefaultPriceProfile
		}
	}
	return result, nil
}

func publicPriceTenants(
	ctx context.Context,
	uow ports.UnitOfWork,
	filter []ids.TenantID,
) ([]ids.TenantID, error) {
	tenants := []ids.TenantID{}
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for after := ids.TenantID(""); ; {
			batch, err := tx.Participants().PublicTenants(ctx, filter, after, priceContextBatchSize)
			if err != nil {
				return err
			}
			tenants = append(tenants, batch...)
			if len(batch) < priceContextBatchSize {
				return nil
			}
			after = batch[len(batch)-1]
		}
	})
	return tenants, err
}

// LoadPriceContexts ограничивает пачку внутреннего API; показ читает единый снимок без разбиения.
func LoadPriceContexts(
	ctx context.Context,
	uow ports.UnitOfWork,
	pricing ports.CatalogPricing,
	buyer ids.UUID,
	known map[ids.TenantID]string,
) (map[ids.TenantID]string, error) {
	return PriceContexts(ctx, uow, batchedPricing{pricing: pricing, known: known}, buyer, nil)
}

func (p batchedPricing) PriceContexts(
	ctx context.Context,
	buyerID ids.UUID,
	tenants []ids.TenantID,
) (map[ids.TenantID]string, error) {
	contexts := make(map[ids.TenantID]string, len(tenants))
	missing := make([]ids.TenantID, 0, len(tenants))
	for _, tenant := range tenants {
		if profile := p.known[tenant]; profile != "" {
			contexts[tenant] = profile
		} else {
			missing = append(missing, tenant)
		}
	}
	for start := 0; start < len(missing); start += priceContextBatchSize {
		end := min(start+priceContextBatchSize, len(missing))
		batch, err := p.pricing.PriceContexts(ctx, buyerID, missing[start:end])
		if err != nil {
			return nil, err
		}
		for tenant, key := range batch {
			contexts[tenant] = key
		}
	}
	return contexts, nil
}

func pricingMode(buyer ids.UUID, profiles map[ids.TenantID]string) string {
	if buyer == "" {
		return ""
	}
	for _, profile := range profiles {
		if profile != "" && profile != catalog.DefaultPriceProfile {
			return "personal"
		}
	}
	return "public"
}
