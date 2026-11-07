package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CatchUpMargin — запас догоняющего прохода перестроения: транзакция,
// начатая до старта перестроения и зафиксированная после, тоже попадёт в него.
const CatchUpMargin = 5 * time.Minute

// Indexer переносит предложения из проекции в индекс. Источник истины —
// проекция: индекс всегда можно собрать заново.
type Indexer struct {
	models       *ModelKeys
	uow          ports.UnitOfWork
	index        ports.OfferIndex
	clock        ports.Clock
	batch        int
	refreshDelay time.Duration
	lock         ports.IndexRebuildLock
}

// RebuildResult — итог перестроения.
type RebuildResult struct {
	Generation string
	Indexed    int
	CaughtUp   int
}

// NewIndexer — синхронизация индекса пачками по batch предложений.
func NewIndexer(
	uow ports.UnitOfWork,
	index ports.OfferIndex,
	clock ports.Clock,
	batch int,
	lock ports.IndexRebuildLock,
	models *ModelKeys,
	refreshDelay time.Duration,
) *Indexer {
	return &Indexer{
		uow:          uow,
		index:        index,
		clock:        clock,
		batch:        batch,
		lock:         lock,
		models:       models,
		refreshDelay: refreshDelay,
	}
}

// HandleTask — обработчик задач синхронизации из outbox.
func (i *Indexer) HandleTask(ctx context.Context, task ports.Task) error {
	tenantID, productID, err := decodeTask(task)
	if err != nil {
		return err
	}

	switch task.Kind {
	case TaskSyncOffer:
		return i.SyncOffer(ctx, tenantID, productID)
	case TaskSyncTenant:
		return i.SyncTenant(ctx, tenantID)
	default:
		return ErrInvalidTask.WithMessage("неизвестная задача: " + task.Kind)
	}
}

// SyncOffer переписывает одно предложение. Товар магазина, который не
// участвует в маркетплейсе, в индекс не попадает.
func (i *Indexer) SyncOffer(ctx context.Context, tenantID ids.TenantID, productID ids.ProductID) error {
	var state ports.OfferState

	err := i.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		state, err = tx.Offers().Offer(ctx, tenantID, productID)
		if err != nil {
			return err
		}
		return tx.Outbox().Enqueue(ctx, ports.StoreCardTask(tenantID, i.clock.Now().Add(i.refreshDelay)))
	})
	if errors.Is(err, catalog.ErrProductNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	ready, err := i.refreshModelKeys(ctx, []ports.OfferState{state})
	if err != nil || len(ready) == 0 {
		return err
	}
	doc, ok := Document(state)
	if !ok {
		return nil
	}

	return i.index.Put(ctx, []ports.OfferDocument{doc})
}

// SyncTenant переписывает все предложения магазина: после смены участника
// или записи реестра меняется видимость всех его товаров.
func (i *Indexer) SyncTenant(ctx context.Context, tenantID ids.TenantID) error {
	var after ids.ProductID

	for {
		var states []ports.OfferState

		err := i.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			var err error
			states, err = tx.Offers().TenantOffers(ctx, tenantID, after, i.batch)
			if err != nil {
				return err
			}
			return tx.Outbox().Enqueue(ctx, ports.StoreCardTask(tenantID, i.clock.Now().Add(i.refreshDelay)))
		})
		if err != nil {
			return err
		}
		if len(states) == 0 {
			return nil
		}

		ready, err := i.refreshModelKeys(ctx, states)
		if err != nil {
			return err
		}
		if docs := documents(ready); len(docs) > 0 {
			if err := i.index.Put(ctx, docs); err != nil {
				return err
			}
		}

		after = states[len(states)-1].Product.State().ProductID
		if len(states) < i.batch {
			return nil
		}
	}
}

// Rebuild собирает индекс заново из проекции: новое поколение, наполнение,
// догоняющий проход и переключение. Поисковый сервис сохраняет Building до
// начала сканирования и направляет новые события в оба поколения.
func (i *Indexer) Rebuild(ctx context.Context) (RebuildResult, error) {
	var result RebuildResult
	err := i.lock.WithLock(ctx, func(ctx context.Context) error {
		var err error
		result, err = i.rebuild(ctx)
		return err
	})
	return result, err
}

func (i *Indexer) rebuild(ctx context.Context) (RebuildResult, error) {
	started := i.clock.Now()

	generation, err := i.index.NewGeneration(ctx)
	if err != nil {
		return RebuildResult{}, err
	}

	result := RebuildResult{Generation: generation}

	participantOffers := func(tx ports.Tx, after ports.OfferKey) ([]ports.OfferState, error) {
		return tx.Offers().ParticipantOffers(ctx, after, i.batch)
	}
	putGeneration := func(docs []ports.OfferDocument) error {
		result.Indexed += len(docs)

		return i.index.PutGeneration(ctx, generation, docs)
	}

	if err := i.pages(ctx, participantOffers, putGeneration); err != nil {
		return result, err
	}

	since := started.Add(-CatchUpMargin)
	changedOffers := func(tx ports.Tx, after ports.OfferKey) ([]ports.OfferState, error) {
		return tx.Offers().ChangedSince(ctx, since, after, i.batch)
	}
	put := func(docs []ports.OfferDocument) error {
		result.CaughtUp += len(docs)

		return i.index.PutGeneration(ctx, generation, docs)
	}

	if err := i.pages(ctx, changedOffers, put); err != nil {
		return result, err
	}

	return result, i.index.Activate(ctx, generation)
}

// EnsureIndex собирает индекс, если рабочего индекса нет: первый запуск или потеря кластера.
func (i *Indexer) EnsureIndex(ctx context.Context) (bool, error) {
	exists, err := i.index.Exists(ctx)
	if err != nil || exists {
		return false, err
	}

	_, err = i.Rebuild(ctx)

	return err == nil, err
}

func (i *Indexer) pages(
	ctx context.Context,
	read func(tx ports.Tx, after ports.OfferKey) ([]ports.OfferState, error),
	write func([]ports.OfferDocument) error,
) error {
	var after ports.OfferKey

	for {
		var states []ports.OfferState

		err := i.uow.Do(ctx, func(_ context.Context, tx ports.Tx) error {
			var err error
			states, err = read(tx, after)
			if err != nil {
				return err
			}
			tenants := map[ids.TenantID]bool{}
			for _, state := range states {
				tenant := state.Product.State().TenantID
				if tenants[tenant] {
					continue
				}
				tenants[tenant] = true
				task := ports.StoreCardTask(tenant, i.clock.Now().Add(i.refreshDelay))
				if err := tx.Outbox().Enqueue(ctx, task); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(states) == 0 {
			return nil
		}

		ready, err := i.refreshModelKeys(ctx, states)
		if err != nil {
			return err
		}
		if docs := documents(ready); len(docs) > 0 {
			if err := write(docs); err != nil {
				return err
			}
		}

		last := states[len(states)-1].Product.State()
		after = ports.OfferKey{TenantID: last.TenantID, ProductID: last.ProductID}

		if len(states) < i.batch {
			return nil
		}
	}
}

func documents(states []ports.OfferState) []ports.OfferDocument {
	docs := make([]ports.OfferDocument, 0, len(states))
	for _, state := range states {
		if doc, ok := Document(state); ok {
			docs = append(docs, doc)
		}
	}

	return docs
}

// Document — документ индекса для предложения участника; ok=false — магазин
// не участвует и в индексе ему делать нечего. Скрытое предложение остаётся
// в индексе документом без карточки: так старая запись не всплывёт.
//
// Версия — сумма версий товара, сигналов, участника и записи реестра. Каждая
// растёт монотонно, снимок читается одним запросом, поэтому более поздний
// снимок даёт версию не меньше прежней, а равная версия — тот же документ.
func Document(state ports.OfferState) (ports.OfferDocument, bool) {
	if state.Product == nil || state.Participant == nil {
		return ports.OfferDocument{}, false
	}
	visibility := participant.VisibilityOf(state.Participant, state.Tenant)
	seller := catalog.Seller{
		Slug:              state.Participant.Slug(),
		Name:              state.Participant.Name(),
		Visible:           visibility.Visible,
		CheckoutAvailable: visibility.CheckoutAvailable,
	}
	offer := catalog.ComposeOffer(state.Product, state.Signals, seller, state.Verdict)
	product := state.Product.State()
	version := state.Product.Version() + state.Participant.Version()
	updatedAt := product.UpdatedAt
	if state.Verdict != nil {
		verdict := state.Verdict.State()
		version += verdict.Version
		if verdict.UpdatedAt.After(updatedAt) {
			updatedAt = verdict.UpdatedAt
		}
	}

	if state.Signals != nil {
		version += state.Signals.Version()
		if signalsAt := state.Signals.State().UpdatedAt; signalsAt.After(updatedAt) {
			updatedAt = signalsAt
		}
	}
	if state.Tenant != nil {
		version += state.Tenant.Version()
	}
	doc := ports.OfferDocument{
		PriceProfiles: product.PriceProfiles,
		ID:            offer.ID.String(),
		TenantID:      string(product.TenantID),
		ProductID:     string(product.ProductID),
		UpdatedAt:     updatedAt,
		Version:       version,
	}

	if !offer.Visible {
		return doc, true
	}

	doc.SellerSlug = offer.Seller.Slug
	doc.SellerName = offer.Seller.Name
	doc.Delivery = state.Participant.State().Delivery
	doc.FreeDeliveryFrom = state.Participant.State().FreeDeliveryFrom.Kopecks()
	doc.Rating = offer.Rating
	doc.Name = offer.Card.Name
	doc.Description = offer.Card.Description
	doc.Image = offer.Card.Image
	doc.Assets = append([]string(nil), offer.Card.Assets...)
	doc.URL = offer.Card.URL
	doc.SKU = offer.Card.SKU
	publication := offer.Card.Publication
	doc.CategoryID = publication.CategoryID
	doc.CategoryPath = publication.CategoryPath
	doc.Attributes = publication.Attributes
	doc.Categories = []catalog.Category{{ID: publication.CategoryID, Name: publication.CategoryName}}
	doc.VariantsID = offer.Card.VariantsID
	doc.Variants = offer.Card.Variants
	doc.ModelKey = offer.Card.ModelKey
	doc.ModelKeySource = offer.Card.ModelKeySource
	doc.Price = offer.Price.Kopecks()
	doc.BasePrice = offer.BasePrice.Kopecks()
	doc.Available = offer.Available
	doc.InStock = offer.InStock
	doc.Visible = offer.Visible
	doc.Listed = offer.Listed
	doc.CheckoutAvailable = offer.CheckoutAvailable

	return doc, true
}

// Maintain возобновляет сборку или перераспределяет выросший каталог.
func (i *Indexer) Maintain(ctx context.Context) (bool, error) {
	needed, err := i.index.NeedsRebuild(ctx)
	if err != nil || !needed {
		return false, err
	}
	// Решение перечитывается под блокировкой: сосед мог уже завершить перенос.
	rebuilt := false
	err = i.lock.WithLock(ctx, func(ctx context.Context) error {
		needed, err := i.index.NeedsRebuild(ctx)
		if err != nil || !needed {
			return err
		}
		_, err = i.rebuild(ctx)
		rebuilt = err == nil
		return err
	})
	return rebuilt, err
}
