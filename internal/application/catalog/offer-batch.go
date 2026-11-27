package catalog

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// SetBatchObserver подключает метрики до запуска обработчика.
func (i *Indexer) SetBatchObserver(observer ports.OfferBatchObserver) {
	i.batchObserver = observer
}

// HandleOfferBatch читает актуальный снимок каждого предложения один раз.
// Задачи не несут старую карточку: повтор и склейка всегда читают последнее состояние.
func (i *Indexer) HandleOfferBatch(ctx context.Context, tasks []ports.Task) []error {
	failures := make([]error, len(tasks))
	keys := make([]ids.OfferID, 0, len(tasks))
	positions := map[ids.OfferID][]int{}
	for n, task := range tasks {
		tenant, product, err := decodeTask(task)
		if err != nil || task.Kind != TaskSyncOffer {
			if err == nil {
				err = ErrInvalidTask
			}
			failures[n] = err
			continue
		}
		key := ids.OfferID{Tenant: tenant, Product: product}
		if len(positions[key]) == 0 {
			keys = append(keys, key)
		}
		positions[key] = append(positions[key], n)
	}
	if len(keys) == 0 {
		return failures
	}
	tiles, err := i.readOfferBatch(ctx, keys)
	var docs []ports.OfferDocument
	if err == nil {
		docs, err = i.indexDocuments(ctx, tiles)
	}
	if err != nil {
		for _, indexes := range positions {
			for _, n := range indexes {
				failures[n] = err
			}
		}
		return failures
	}

	// Ошибка записи плитки возвращается и задачам изменённых SKU этой плитки.
	for _, tile := range tiles {
		key := tileOfferID(tile.Key)
		for _, member := range tile.Members {
			if member.Changed {
				p := member.State.Product.State()
				sku := ids.OfferID{Tenant: p.TenantID, Product: p.ProductID}
				positions[key] = append(positions[key], positions[sku]...)
			}
		}
	}
	// Плитка следует за последней задачей об изменении её состава.
	slices.SortStableFunc(docs, func(left, right ports.OfferDocument) int {
		leftPosition := slices.Max(positions[indexDocumentKey(left)])
		rightPosition := slices.Max(positions[indexDocumentKey(right)])
		return cmp.Compare(leftPosition, rightPosition)
	})
	i.sendOfferBatch(ctx, docs, positions, failures)
	return failures
}

func (i *Indexer) readOfferBatch(
	ctx context.Context,
	keys []ids.OfferID,
) ([]ports.TileState, error) {
	var offers []ids.OfferID
	var former []ports.TileKey
	for _, key := range keys {
		if tile, ok := formerTileKey(key.Product); ok {
			former = append(former, ports.TileKey{TenantID: key.Tenant, Key: tile})
		} else {
			offers = append(offers, key)
		}
	}
	var tiles []ports.TileState
	err := i.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		tiles, err = tx.Offers().Tiles(ctx, offers, former)
		if err != nil {
			return err
		}
		return i.scheduleStoreSummaryChanges(ctx, tx, changedTileMembers(tiles))
	})
	return tiles, err
}

func (i *Indexer) sendOfferBatch(
	ctx context.Context,
	docs []ports.OfferDocument,
	positions map[ids.OfferID][]int,
	failures []error,
) {
	if len(docs) == 0 {
		return
	}
	started := time.Now()
	err := ctx.Err()
	if err == nil {
		err = i.put(ctx, docs)
		if i.batchObserver != nil {
			i.batchObserver.ObserveBatch(len(docs), time.Since(started), err)
		}
	}
	var capacity *ports.OfferCapacityError
	if errors.As(err, &capacity) {
		for _, key := range capacity.Deferred {
			for _, n := range positions[key] {
				failures[n] = ports.ErrOfferCapacity
			}
		}
		docs = docs[capacity.UnconfirmedFrom:]
		err = capacity.UnconfirmedError
	}
	// Только постоянный отказ требует дробления. Сетевой сбой оставляет
	// неподтверждённые документы на повтор с отсрочкой.
	if errs.CodeOf(err) == errs.Invalid && len(docs) > 1 {
		middle := len(docs) / 2
		i.sendOfferBatch(ctx, docs[:middle], positions, failures)
		i.sendOfferBatch(ctx, docs[middle:], positions, failures)
		return
	}
	if err == nil {
		return
	}
	for _, doc := range docs {
		for _, n := range positions[indexDocumentKey(doc)] {
			failures[n] = err
		}
	}
}

func indexDocumentKey(doc ports.OfferDocument) ids.OfferID {
	return ids.OfferID{Tenant: ids.TenantID(doc.TenantID), Product: ids.ProductID(doc.ProductID)}
}
