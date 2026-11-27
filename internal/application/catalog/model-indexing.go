package catalog

import (
	"context"
	"log/slog"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Версионное сохранение не позволяет пересборке затереть более новое событие.
// Конфликтующий снимок не индексируется: актуальное событие догонит его через outbox.
func (i *Indexer) refreshModelKeys(ctx context.Context, states []ports.OfferState) ([]ports.OfferState, error) {
	var definition taxonomy.Definition
	if i.models != nil && i.models.enabled {
		err := i.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			var err error
			definition, err = tx.Taxonomy().Current(ctx)
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	models := i.models.forBatch(ctx, states, definition)
	changed := map[ids.OfferID]bool{}
	products := []*catalog.Product{}
	for _, state := range states {
		if state.Product != nil && models.Apply(ctx, state.Product, definition) {
			p := state.Product.State()
			key := ids.OfferID{Tenant: p.TenantID, Product: p.ProductID}
			if !changed[key] {
				products = append(products, state.Product)
				changed[key] = true
			}
		}
	}
	saved, err := i.saveModelKeys(ctx, products)
	if err != nil {
		return nil, err
	}
	ready := make([]ports.OfferState, 0, len(states))
	for _, state := range states {
		if state.Product != nil {
			p := state.Product.State()
			key := ids.OfferID{Tenant: p.TenantID, Product: p.ProductID}
			if changed[key] && !saved[key] {
				i.modelKeyConflict(key)
				continue
			}
		}
		ready = append(ready, state)
	}
	return ready, nil
}

func (i *Indexer) saveModelKeys(ctx context.Context, products []*catalog.Product) (map[ids.OfferID]bool, error) {
	saved := map[ids.OfferID]bool{}
	if len(products) == 0 {
		return saved, nil
	}
	err := i.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		keys, err := tx.Products().SaveIdentities(ctx, products)
		if err != nil {
			return err
		}
		tasks := make([]ports.NewTask, 0, len(keys))
		for _, key := range keys {
			saved[key] = true
			tasks = append(tasks, SyncOfferTask(key.Tenant, key.Product, i.clock.Now()))
		}
		return tx.Outbox().EnqueueBatch(ctx, tasks)
	})
	return saved, err
}

func (i *Indexer) modelKeyConflict(key ids.OfferID) {
	slog.Warn("конфликт расчёта ключа модели", "tenantId", key.Tenant, "productId", key.Product)
	if i.models != nil && i.models.observer != nil {
		i.models.observer.Conflict()
	}
}
