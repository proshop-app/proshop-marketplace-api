package catalog

import (
	"context"
	"slices"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// deferredWrites — сохранения пачки до одного оператора на таблицу. Агрегат в очереди один раз:
// он несёт последнее состояние, а исходная версия не меняется до записи. Чтение из базы ключа,
// у которого есть отложенная запись, сначала дописывает очередь; чужие ключи чтение не трогает.
type deferredWrites struct {
	products      catalog.ProductRepository
	verdicts      moderation.VerdictRepository
	queuedProduct []*catalog.Product
	queuedVerdict []*moderation.Verdict
}

// batchOffers — снимки предложений из базы с учётом отложенных записей пачки.
type batchOffers struct {
	ports.OfferStates
	writes *deferredWrites
}

// Save откладывает запись товара до конца пачки.
func (r *batchProducts) Save(_ context.Context, product *catalog.Product) error {
	if !slices.Contains(r.writes.queuedProduct, product) {
		r.writes.queuedProduct = append(r.writes.queuedProduct, product)
	}
	return nil
}

// Save откладывает запись вердикта до конца пачки.
func (r *batchVerdicts) Save(_ context.Context, verdict *moderation.Verdict) error {
	if !slices.Contains(r.writes.queuedVerdict, verdict) {
		r.writes.queuedVerdict = append(r.writes.queuedVerdict, verdict)
	}
	return nil
}

func (w *deferredWrites) flush(ctx context.Context) error {
	if len(w.queuedProduct) > 0 {
		if err := w.products.SaveMany(ctx, w.queuedProduct); err != nil {
			return err
		}
		w.queuedProduct = nil
	}
	if len(w.queuedVerdict) > 0 {
		if err := w.verdicts.SaveMany(ctx, w.queuedVerdict); err != nil {
			return err
		}
		w.queuedVerdict = nil
	}
	return nil
}

// flushKey дописывает очередь, только если в ней есть запись этого ключа.
func (w *deferredWrites) flushKey(ctx context.Context, key ids.OfferID) error {
	if w.queued(key) {
		return w.flush(ctx)
	}
	return nil
}

// flushKeys — flushKey для списка ключей.
func (w *deferredWrites) flushKeys(ctx context.Context, keys []ids.OfferID) error {
	if slices.ContainsFunc(keys, w.queued) {
		return w.flush(ctx)
	}
	return nil
}

func (w *deferredWrites) queued(key ids.OfferID) bool {
	productKey := func(product *catalog.Product) bool {
		s := product.State()
		return s.TenantID == key.Tenant && s.ProductID == key.Product
	}
	verdictKey := func(verdict *moderation.Verdict) bool {
		s := verdict.State()
		return s.TenantID == key.Tenant && s.ProductID == key.Product
	}
	return slices.ContainsFunc(w.queuedProduct, productKey) || slices.ContainsFunc(w.queuedVerdict, verdictKey)
}

func (o *batchOffers) Offer(ctx context.Context, tenant ids.TenantID, product ids.ProductID) (ports.OfferState, error) {
	if err := o.writes.flushKey(ctx, ids.OfferID{Tenant: tenant, Product: product}); err != nil {
		return ports.OfferState{}, err
	}
	return o.OfferStates.Offer(ctx, tenant, product)
}

func (o *batchOffers) Batch(ctx context.Context, keys []ids.OfferID) ([]ports.OfferState, error) {
	if err := o.writes.flushKeys(ctx, keys); err != nil {
		return nil, err
	}
	return o.OfferStates.Batch(ctx, keys)
}

func (o *batchOffers) StatusBatch(ctx context.Context, keys []ids.OfferID) ([]ports.OfferStatus, error) {
	if err := o.writes.flushKeys(ctx, keys); err != nil {
		return nil, err
	}
	return o.OfferStates.StatusBatch(ctx, keys)
}

// Обход магазина или всех участников может задеть любой ключ пачки: очередь дописывается целиком.
func (o *batchOffers) WalkStoreSummaryOffers(
	ctx context.Context,
	tenant ids.TenantID,
	walk func([]ports.OfferState) error,
) error {
	if err := o.writes.flush(ctx); err != nil {
		return err
	}
	return o.OfferStates.WalkStoreSummaryOffers(ctx, tenant, walk)
}

func (o *batchOffers) TenantOffers(
	ctx context.Context,
	tenant ids.TenantID,
	after ids.ProductID,
	limit int,
) ([]ports.OfferState, error) {
	if err := o.writes.flush(ctx); err != nil {
		return nil, err
	}
	return o.OfferStates.TenantOffers(ctx, tenant, after, limit)
}

func (o *batchOffers) ParticipantOffers(
	ctx context.Context,
	after ports.OfferKey,
	limit int,
) ([]ports.OfferState, error) {
	if err := o.writes.flush(ctx); err != nil {
		return nil, err
	}
	return o.OfferStates.ParticipantOffers(ctx, after, limit)
}

func (o *batchOffers) ChangedSince(
	ctx context.Context,
	since time.Time,
	after ports.OfferKey,
	limit int,
) ([]ports.OfferState, error) {
	if err := o.writes.flush(ctx); err != nil {
		return nil, err
	}
	return o.OfferStates.ChangedSince(ctx, since, after, limit)
}
