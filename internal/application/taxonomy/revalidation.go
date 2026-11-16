package taxonomy

import (
	"context"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// RevalidateNext фиксирует не больше одной страницы вместе с прогрессом и outbox.
// Текущая голова под FOR SHARE согласует страницу с публикацией, как и приём события.
func (s *Service) RevalidateNext(ctx context.Context) (*taxonomy.Revalidation, error) {
	var job *taxonomy.Revalidation
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		job, err = tx.Taxonomy().NextRevalidation(ctx)
		if err != nil || job == nil {
			return err
		}
		definition, err := tx.Taxonomy().Current(ctx)
		if err != nil {
			return err
		}
		items, err := tx.Products().RevalidationPage(ctx, *job, revalidationBatch)
		if err != nil {
			return err
		}
		for _, product := range items {
			if err := s.revalidateProduct(ctx, tx, product, definition); err != nil {
				return err
			}
			state := product.State()
			job.TenantCursor = string(state.TenantID)
			job.ProductCursor = string(state.ProductID)
		}
		job.Processed += int64(len(items))
		job.Done = len(items) < revalidationBatch
		return tx.Taxonomy().AdvanceRevalidation(ctx, *job)
	})
	if err != nil {
		return nil, err
	}
	return job, nil
}

func (s *Service) revalidateProduct(
	ctx context.Context,
	tx ports.Tx,
	product *catalog.Product,
	definition taxonomy.Definition,
) error {
	state := product.State()
	// Событие уже проверило актуальный снимок; повторная страница не создаёт лишних задач.
	if state.Card.Publication.Version >= definition.Version || !product.Revalidate(definition, s.clock.Now()) {
		return nil
	}
	if err := tx.Products().Save(ctx, product); err != nil {
		return err
	}
	status := moderationstatus.OfferTask(state.TenantID, state.ProductID, s.clock.Now())
	if err := tx.Outbox().Enqueue(ctx, status); err != nil {
		return err
	}
	if s.evaluator != nil {
		if err := s.evaluator.Evaluate(ctx, tx, product, nil, nil, s.clock.Now()); err != nil {
			return err
		}
	}
	return tx.Outbox().Enqueue(ctx, appcatalog.SyncOfferTask(state.TenantID, state.ProductID, s.clock.Now()))
}
