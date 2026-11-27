package catalog

import (
	"context"
	"errors"
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
	states, err := i.readOfferBatch(ctx, keys)
	if err == nil {
		states, err = i.refreshModelKeys(ctx, states)
	}
	if err != nil {
		for _, indexes := range positions {
			for _, n := range indexes {
				failures[n] = err
			}
		}
		return failures
	}

	// Порядок SQL не определён; восстанавливаем справедливый порядок взятых задач.
	byKey := map[ids.OfferID]ports.OfferDocument{}
	for _, state := range states {
		if doc, ok := Document(state); ok {
			key := ids.OfferID{Tenant: ids.TenantID(doc.TenantID), Product: ids.ProductID(doc.ProductID)}
			if previous, exists := byKey[key]; !exists || doc.Version >= previous.Version {
				byKey[key] = doc
			}
		}
	}
	docs := make([]ports.OfferDocument, 0, len(byKey))
	for _, key := range keys {
		if doc, ok := byKey[key]; ok {
			docs = append(docs, doc)
		}
	}
	i.sendOfferBatch(ctx, docs, positions, failures)
	return failures
}

func (i *Indexer) readOfferBatch(
	ctx context.Context,
	keys []ids.OfferID,
) ([]ports.OfferState, error) {
	var states []ports.OfferState
	err := i.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		states, err = tx.Offers().Batch(ctx, keys)
		if err != nil {
			return err
		}
		return i.scheduleStoreSummaryChanges(ctx, tx, states)
	})
	return states, err
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
		err = i.index.Put(ctx, docs)
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
	for _, doc := range docs {
		key := ids.OfferID{Tenant: ids.TenantID(doc.TenantID), Product: ids.ProductID(doc.ProductID)}
		for _, n := range positions[key] {
			failures[n] = err
		}
	}
}
