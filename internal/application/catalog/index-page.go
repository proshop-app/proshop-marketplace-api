package catalog

import (
	"context"
	"log/slog"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

type skippedOffer struct {
	doc ports.OfferDocument
	err error
}

// SetSkipObserver подключает метрику пропусков до запуска индексатора.
func (i *Indexer) SetSkipObserver(observer ports.OfferIndexSkipObserver) {
	i.skipObserver = observer
}

func (i *Indexer) writeIndexPage(
	ctx context.Context,
	states []ports.TileState,
	put func([]ports.OfferDocument) error,
) (int, []skippedOffer, error) {
	if len(states) == 0 {
		return 0, nil, nil
	}
	docs, err := i.indexDocuments(ctx, states)
	if err != nil {
		return 0, nil, err
	}
	return writeIndexBatch(ctx, docs, put)
}

func writeIndexBatch(
	ctx context.Context,
	docs []ports.OfferDocument,
	put func([]ports.OfferDocument) error,
) (int, []skippedOffer, error) {
	if len(docs) == 0 {
		return 0, nil, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}

	err := put(docs)
	if err == nil {
		return len(docs), nil, nil
	}
	if errs.CodeOf(err) != errs.Invalid {
		return 0, nil, err
	}
	if len(docs) == 1 {
		return 0, []skippedOffer{{doc: docs[0], err: err}}, nil
	}

	middle := len(docs) / 2
	left, skippedLeft, err := writeIndexBatch(ctx, docs[:middle], put)
	if err != nil {
		return 0, nil, err
	}
	right, skippedRight, err := writeIndexBatch(ctx, docs[middle:], put)
	if err != nil {
		return 0, nil, err
	}
	return left + right, append(skippedLeft, skippedRight...), nil
}

func (i *Indexer) observeIndexSkips(ctx context.Context, phase, generation string, skipped []skippedOffer) {
	if len(skipped) == 0 {
		return
	}
	if i.skipObserver != nil {
		i.skipObserver.ObserveIndexSkips(phase, len(skipped))
	}
	for _, offer := range skipped {
		slog.WarnContext(
			ctx,
			"предложение пропущено при индексации",
			"phase",
			phase,
			"generation",
			generation,
			"tenantId",
			offer.doc.TenantID,
			"productId",
			offer.doc.ProductID,
			"version",
			offer.doc.Version,
			"error",
			offer.err,
		)
	}
}
