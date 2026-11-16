package catalog

import (
	"context"
	"errors"
	"slices"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const priceContextBatchSize = 100

type batchedPricing struct {
	pricing ports.CatalogPricing
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

func (q *Queries) priceOffer(
	ctx context.Context,
	offerID ids.OfferID,
	doc *ports.OfferDocument,
	buyerID ids.UUID,
	contexts map[ids.TenantID]string,
	publicStates map[ids.OfferID]ports.OfferState,
) error {
	key := priceProfileKey(contexts, offerID.Tenant)
	// На холодном отказе персонализации остаётся публичная карточка из проекции.
	// Оформление независимо перепроверит цену и наличие у продавца.
	if buyerID != "" && key == "" {
		state := publicStates[offerID]
		var err error
		// Группа передаёт снимки одной пачки; карточка читает свою проекцию отдельно.
		if publicStates == nil {
			err = q.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				var err error
				state, err = tx.Offers().Offer(ctx, offerID.Tenant, offerID.Product)
				return err
			})
		}
		if errors.Is(err, catalog.ErrProductNotFound) {
			return catalog.ErrOfferNotFound
		}
		if err != nil {
			return err
		}
		// Пустой контекст возможен и для скрытого магазина: устаревший индекс не должен его раскрыть.
		public, ok := Document(state)
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
	return pricing.PriceContexts(ctx, buyerID, tenants)
}

// LoadPriceContexts ограничивает пачку внутреннего API; показ читает единый снимок без разбиения.
func LoadPriceContexts(
	ctx context.Context,
	uow ports.UnitOfWork,
	pricing ports.CatalogPricing,
	buyer ids.UUID,
) (map[ids.TenantID]string, error) {
	return PriceContexts(ctx, uow, batchedPricing{pricing: pricing}, buyer, nil)
}

func (p batchedPricing) PriceContexts(
	ctx context.Context,
	buyerID ids.UUID,
	tenants []ids.TenantID,
) (map[ids.TenantID]string, error) {
	contexts := make(map[ids.TenantID]string, len(tenants))
	for start := 0; start < len(tenants); start += priceContextBatchSize {
		end := min(start+priceContextBatchSize, len(tenants))
		batch, err := p.pricing.PriceContexts(ctx, buyerID, tenants[start:end])
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
