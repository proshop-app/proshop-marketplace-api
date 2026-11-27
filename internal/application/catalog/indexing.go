package catalog

import (
	"context"
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
	maintenanceSignal func()
	batchObserver     ports.OfferBatchObserver
	skipObserver      ports.OfferIndexSkipObserver
	models            *ModelKeys
	uow               ports.UnitOfWork
	index             ports.OfferIndex
	clock             ports.Clock
	batch             int
	refreshDelay      time.Duration
	lock              ports.IndexRebuildLock
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

		return i.syncTenantTaskPage(ctx, tenantID)
	default:

		return ErrInvalidTask.WithMessage("неизвестная задача: " + task.Kind)
	}
}

// SyncOffer переписывает одно предложение. Товар магазина, который не
// участвует в маркетплейсе, в индекс не попадает.
func (i *Indexer) SyncOffer(ctx context.Context, tenantID ids.TenantID, productID ids.ProductID) error {
	task := SyncOfferTask(tenantID, productID, i.clock.Now())
	if key, former := formerTileKey(productID); former {
		task = syncFormerTileTask(tenantID, key, i.clock.Now())
	}
	return i.HandleOfferBatch(ctx, []ports.Task{{
		Kind:     task.Kind,
		TenantID: task.TenantID,
		Payload:  task.Payload,
	}})[0]
}

func (i *Indexer) scheduleStoreSummaryChanges(ctx context.Context, tx ports.Tx, states []ports.OfferState) error {
	var keys []ids.OfferID
	for _, state := range states {
		if state.Participant != nil && state.StoreSummaryPending {
			p := state.Product.State()
			keys = append(keys, ids.OfferID{Tenant: p.TenantID, Product: p.ProductID})
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return tx.StoreCards().ScheduleChanges(ctx, keys, i.clock.Now().Add(i.refreshDelay))
}

// SyncTenant переписывает все предложения магазина: после смены участника
// или записи реестра меняется видимость всех его товаров.
func (i *Indexer) SyncTenant(ctx context.Context, tenantID ids.TenantID) error {
	var after ids.ProductID
	put := func(docs []ports.OfferDocument) error {
		return i.put(ctx, docs)
	}

	for {
		var states []ports.TileState

		err := i.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			var err error
			states, err = tx.Offers().TilePage(ctx, ports.TilePage{
				TenantID: tenantID,
				After:    ports.TileKey{Key: string(after)},
				Limit:    i.batch,
			})
			if err != nil || after != "" {
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

		_, skipped, err := i.writeIndexPage(ctx, states, put)
		if err != nil {
			return err
		}
		i.observeIndexSkips(ctx, "tenant", "", skipped)

		after = ids.ProductID(states[len(states)-1].Key.Key)

		if len(states) < i.batch {
			return nil
		}
	}
}

// BuyerOffer — предложение, как его видит покупатель: продавец — по участию и записи реестра,
// цена, наличие и видимость — catalog.ComposeOffer. Документ поиска и корзина считают его одним
// кодом, поэтому не расходятся. ok=false — магазин не участвует.
func BuyerOffer(state ports.OfferState) (catalog.Offer, bool) {
	if state.Product == nil || state.Participant == nil {
		return catalog.Offer{}, false
	}
	visibility := participant.VisibilityOf(state.Participant, state.Tenant)
	seller := catalog.Seller{
		Slug:              state.Participant.Slug(),
		Name:              state.Participant.Name(),
		Visible:           visibility.Visible,
		CheckoutAvailable: visibility.CheckoutAvailable,
	}
	return catalog.ComposeOffer(state.Product, state.Signals, seller, state.Verdict), true
}

// Document — документ индекса для предложения участника; ok=false — магазин
// не участвует и в индексе ему делать нечего. Скрытое предложение остаётся
// в индексе документом без карточки: так старая запись не всплывёт.
//
// Версия — сумма версий товара, сигналов, участника и записи реестра. Каждая
// растёт монотонно, снимок читается одним запросом, поэтому более поздний
// снимок даёт версию не меньше прежней, а равная версия — тот же документ.
func Document(state ports.OfferState) (ports.OfferDocument, bool) {
	offer, ok := BuyerOffer(state)
	if !ok {
		return ports.OfferDocument{}, false
	}
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
		ID:        offer.ID.String(),
		TenantID:  string(product.TenantID),
		ProductID: string(product.ProductID),
		UpdatedAt: updatedAt,
		Version:   version,
	}

	if !offer.Visible {
		return doc, true
	}

	doc.SellerSlug = offer.Seller.Slug
	doc.SellerName = offer.Seller.Name
	doc.Delivery = state.Participant.State().Delivery
	doc.Rating = offer.Rating
	doc.SalesTier = salesTier(product.SalesCount)
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
	doc.AttributeSearchTerms = publication.AttributeSearchTerms
	doc.AttributeFacetValues = publication.AttributeFacetValues
	doc.Categories = []catalog.Category{{ID: publication.CategoryID, Name: publication.CategoryName}}
	doc.VariantsID = offer.Card.VariantsID
	doc.TileKey = offer.Card.TileKey
	doc.Variants = offer.Card.Variants
	doc.ModelKey = offer.Card.ModelKey
	doc.ModelKeySource = offer.Card.ModelKeySource
	doc.VariantKey = offer.Card.VariantKey
	doc.Price = offer.Price.Kopecks()
	doc.BasePrice = offer.BasePrice.Kopecks()
	doc.Available = offer.Available
	doc.InStock = offer.InStock
	doc.Visible = offer.Visible
	doc.Listed = offer.Listed
	doc.CheckoutAvailable = offer.CheckoutAvailable

	return doc, true
}

// SetMaintenanceSignal подключает запуск обслуживания после обращений по записи.
func (i *Indexer) SetMaintenanceSignal(signal func()) {
	i.maintenanceSignal = signal
}

func (i *Indexer) put(ctx context.Context, docs []ports.OfferDocument) error {
	err := i.index.Put(ctx, docs)
	if i.maintenanceSignal != nil && len(docs) > 0 {
		i.maintenanceSignal()
	}
	return err
}

// Maintain собирает отсутствующий индекс (первый запуск или потеря кластера),
// возобновляет сборку или перераспределяет выросший каталог. Сборка идёт в фоне
// под общей advisory lock: её ведёт одна реплика, старт процесса и выдача
// остальным путям от поиска не зависят.
func (i *Indexer) Maintain(ctx context.Context) (bool, error) {
	needed, err := i.rebuildNeeded(ctx)
	if err != nil || !needed {
		return false, err
	}
	// Решение перечитывается под блокировкой: сосед мог уже завершить сборку.
	rebuilt := false
	err = i.lock.WithLock(ctx, func(ctx context.Context) error {
		needed, err := i.rebuildNeeded(ctx)
		if err != nil || !needed {
			return err
		}
		_, err = i.rebuild(ctx)
		rebuilt = err == nil
		return err
	})
	return rebuilt, err
}

// rebuildNeeded — рабочего поколения нет либо поиск просит перенос.
func (i *Indexer) rebuildNeeded(ctx context.Context) (bool, error) {
	exists, err := i.index.Exists(ctx)
	if err != nil || !exists {
		return err == nil, err
	}
	return i.index.NeedsRebuild(ctx)
}
