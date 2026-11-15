package catalog

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const offersConcurrency = 5

var errOffersUnavailable = errs.New(errs.Unavailable, "catalog.offers_unavailable", "предложения недоступны")

// Offers возвращает доступные карточки в порядке входа, без загрузки вариантов.
func (q *Queries) Offers(ctx context.Context, offerIDs []ids.OfferID, buyerID ids.UUID) ([]OfferCard, error) {
	docs := make([]ports.OfferDocument, len(offerIDs))
	group, readCtx := errgroup.WithContext(ctx)
	group.SetLimit(offersConcurrency)
	for i, id := range offerIDs {
		group.Go(func() error {
			doc, found, err := q.index.Get(readCtx, id.String())
			if err != nil {
				return errOffersUnavailable.Wrap(err)
			}
			if found && doc.Visible && doc.Listed {
				docs[i] = doc
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}

	tenants := make([]ids.TenantID, 0)
	seen := make(map[ids.TenantID]bool)
	for i, doc := range docs {
		tenant := offerIDs[i].Tenant
		if doc.Visible && !seen[tenant] {
			tenants = append(tenants, tenant)
			seen[tenant] = true
		}
	}
	contexts, err := PriceContexts(ctx, q.uow, q.platform, buyerID, tenants)
	if err != nil {
		return nil, err
	}
	cards := make([]OfferCard, 0, len(docs))
	for i, doc := range docs {
		key := priceProfileKey(contexts, offerIDs[i].Tenant)
		// Как у карточки: на холодном отказе персонализации покупатель видит публичную цену.
		if buyerID != "" && key == "" {
			key = catalog.DefaultPriceProfile
		}
		if !doc.Visible || !selectOfferPrice(&doc, key) {
			continue
		}
		cards = append(cards, OfferCard{Offer: doc})
	}
	return cards, nil
}

func selectOfferPrice(doc *ports.OfferDocument, key string) bool {
	profile, found := catalog.SelectPrice(doc.PriceProfiles, key)
	if !found {
		return false
	}
	doc.Price = profile.EffectivePrice.Kopecks()
	doc.BasePrice = profile.Price.Kopecks()
	return true
}
