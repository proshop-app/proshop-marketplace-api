package catalog

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const offersConcurrency = 5

var errOffersUnavailable = errs.New(errs.Unavailable, "catalog.offers_unavailable", "предложения недоступны")

// Offers возвращает доступные карточки в порядке входа, без загрузки вариантов.
func (q *Queries) Offers(ctx context.Context, offerIDs []ids.OfferID) ([]OfferCard, error) {
	docs := make([]ports.OfferDocument, len(offerIDs))
	group, readCtx := errgroup.WithContext(ctx)
	group.SetLimit(offersConcurrency)
	for i, id := range offerIDs {
		group.Go(func() error {
			doc, found, err := q.index.Get(readCtx, id.String())
			if err != nil {
				return errOffersUnavailable.Wrap(err)
			}
			if found && doc.Visible {
				docs[i] = doc
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}

	cards := make([]OfferCard, 0, len(docs))
	for _, doc := range docs {
		if !doc.Visible {
			continue
		}
		cards = append(cards, OfferCard{Offer: doc})
	}
	return cards, nil
}
