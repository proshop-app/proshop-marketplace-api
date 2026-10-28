package taxonomy

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// Apply публикует готовую поставку вместе с историей и задачами перепроверки товаров.
func (s *Service) Apply(ctx context.Context, release taxonomy.Release) (taxonomy.ReleaseResult, error) {
	return s.changeRelease(ctx, release, false)
}

// Rollback восстанавливает предыдущие данные новой монотонной версией.
func (s *Service) Rollback(ctx context.Context, release taxonomy.Release) (taxonomy.ReleaseResult, error) {
	return s.changeRelease(ctx, release, true)
}

func (s *Service) changeRelease(
	ctx context.Context,
	release taxonomy.Release,
	rollback bool,
) (taxonomy.ReleaseResult, error) {
	var result taxonomy.ReleaseResult
	if err := taxonomy.ValidateRelease(release); err != nil {
		return result, err
	}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		repository := tx.Taxonomy()
		if err := repository.Lock(ctx); err != nil {
			return err
		}
		current, err := repository.Current(ctx)
		if err != nil {
			return err
		}
		latest, err := repository.LatestRelease(ctx)
		if err != nil {
			return err
		}
		var state *taxonomy.ReleaseState
		if latest != nil {
			state = &latest.ReleaseState
		}
		result = taxonomy.ReleaseResult{
			Release:         release.Version,
			SnapshotVersion: current.Version,
			RolledBack:      rollback,
		}
		plan := taxonomy.PlanRelease
		if rollback {
			plan = taxonomy.PlanTaxonomyRollback
		}
		changed, err := plan(current, state, release)
		if err != nil || !changed {
			return err
		}
		next := taxonomy.Definition{
			Version:    current.Version + 1,
			Categories: release.Categories,
		}
		if rollback {
			next.Categories = latest.Previous.Categories
		}
		if err := next.CheckEvolution(current); err != nil {
			return err
		}
		if err := repository.Save(ctx, next, current.Version); err != nil {
			return err
		}
		if rollback {
			err = repository.MarkRollback(ctx, release.Version, next.Version)
		} else {
			err = repository.RecordRelease(ctx, release, next.Version, current)
		}
		if err != nil {
			return err
		}
		if err := s.revalidate(ctx, tx, next); err != nil {
			return err
		}
		result.SnapshotVersion = next.Version
		result.Changed = true
		return nil
	})
	return result, err
}
