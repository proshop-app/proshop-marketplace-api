package catalog

import (
	"context"
	"errors"
	"log/slog"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
)

// Версионное сохранение не позволяет пересборке затереть более новое событие.
// Конфликтующий снимок не индексируется: актуальное событие догонит его через outbox.
func (i *Indexer) refreshModelKeys(ctx context.Context, states []ports.OfferState) ([]ports.OfferState, error) {
	ready := make([]ports.OfferState, 0, len(states))
	for _, state := range states {
		if state.Product != nil && i.models.Apply(ctx, state.Product) {
			err := i.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				return tx.Products().Save(ctx, state.Product)
			})
			if errors.Is(err, catalog.ErrConflict) {
				product := state.Product.State()
				slog.Warn("конфликт расчёта ключа модели", "tenantId", product.TenantID, "productId", product.ProductID)
				if i.models != nil && i.models.observer != nil {
					i.models.observer.Conflict()
				}
				continue
			}
			if err != nil {
				return nil, err
			}
		}
		ready = append(ready, state)
	}

	return ready, nil
}
