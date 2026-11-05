// Package taxonomy — загрузка версий схем и чтение таксономии для админок магазинов.
package taxonomy

import (
	"context"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const revalidationBatch = 200

// Service атомарно меняет схему и перепроверяет проекции; индекс догоняет через outbox.
type Service struct {
	evaluator ports.ModerationEvaluator
	uow       ports.UnitOfWork
	clock     ports.Clock
}

// NewService собирает сценарии поверх транзакций и часов.
func NewService(uow ports.UnitOfWork, clock ports.Clock, evaluators ...ports.ModerationEvaluator) *Service {
	var evaluator ports.ModerationEvaluator
	if len(evaluators) > 0 {
		evaluator = evaluators[0]
	}
	return &Service{uow: uow, clock: clock, evaluator: evaluator}
}

// Current возвращает согласованный снимок текущей версии.
func (s *Service) Current(ctx context.Context) (taxonomy.Definition, error) {
	var d taxonomy.Definition
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		d, err = tx.Taxonomy().Current(ctx)
		return err
	})
	if d.Categories == nil {
		d.Categories = []taxonomy.Category{}
	}
	if d.Attributes == nil {
		d.Attributes = []taxonomy.Attribute{}
	}
	if d.Schemas == nil {
		d.Schemas = map[string]taxonomy.Schema{}
	}
	return d, err
}

// LoadPart соединяет часть с текущим снимком под той же блокировкой, что и публикация.
func (s *Service) LoadPart(
	ctx context.Context,
	part string,
	input taxonomy.Definition,
	expected int64,
) (int64, error) {
	var version int64
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		r := tx.Taxonomy()
		if err := r.Lock(ctx); err != nil {
			return err
		}
		current, err := r.Current(ctx)
		if err != nil {
			return err
		}
		if expected != current.Version || current.Version >= taxonomy.MaxTaxonomyVersion {
			return taxonomy.ErrConflict
		}
		next := current
		next.Version++
		switch part {
		case "attributes":
			next.Attributes = input.Attributes
		case "schemas":
			next.Schemas = input.Schemas
		case "categories":
			next.Categories = input.Categories
		default:
			return taxonomy.ErrInvalid
		}
		if err := next.Validate(); err != nil {
			return err
		}
		if err := next.CheckEvolution(current); err != nil {
			return err
		}
		if err := resolveDictionaries(ctx, tx.Reference(), &next); err != nil {
			return err
		}
		if err := r.Save(ctx, next, current.Version); err != nil {
			return err
		}
		if err := s.revalidate(ctx, tx, next); err != nil {
			return err
		}
		version = next.Version
		return nil
	})
	return version, err
}

func (s *Service) revalidate(ctx context.Context, tx ports.Tx, d taxonomy.Definition) error {
	var tenant ids.TenantID
	var product ids.ProductID
	for {
		items, err := tx.Products().Page(ctx, tenant, product, revalidationBatch)
		if err != nil {
			return err
		}
		for _, p := range items {
			state := p.State()
			tenant, product = state.TenantID, state.ProductID
			if p.Revalidate(d, s.clock.Now()) {
				if err := tx.Products().Save(ctx, p); err != nil {
					return err
				}
			}
			if s.evaluator != nil {
				if err := s.evaluator.Evaluate(ctx, tx, p, nil, s.clock.Now()); err != nil {
					return err
				}
			}
			task := appcatalog.SyncOfferTask(tenant, product, s.clock.Now())
			if err := tx.Outbox().Enqueue(ctx, task); err != nil {
				return err
			}
		}
		if len(items) < revalidationBatch {
			return nil
		}
	}
}
