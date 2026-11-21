package taxonomy

import (
	"context"
	"maps"
	"reflect"
	"slices"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// SelectionRefresh проверяет ревизии подтверждённых пулов между вызовами фонового цикла.
type SelectionRefresh struct {
	service   *Service
	revisions map[string]int64
}

type selectionUpdate struct {
	version int64
	heads   map[string]int64
	values  map[string]taxonomy.DictionaryValues
}

// NewSelectionRefresh создаёт состояние одного фонового цикла.
func (s *Service) NewSelectionRefresh() *SelectionRefresh {
	return &SelectionRefresh{service: s}
}

// RunOnce публикует изменения подтверждённых записей обычной версией таксономии.
func (s *SelectionRefresh) RunOnce(ctx context.Context) error {
	update, err := s.observe(ctx)
	if err != nil {
		return err
	}
	if len(update.values) > 0 {
		applied, err := s.publish(ctx, update)
		if !applied || err != nil {
			return err
		}
	}
	s.revisions = update.heads
	return nil
}

func (s *SelectionRefresh) observe(ctx context.Context) (selectionUpdate, error) {
	var update selectionUpdate
	err := s.service.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		current, err := tx.Taxonomy().Current(ctx)
		if err != nil || len(current.SelectionValues) == 0 {
			return err
		}
		registry := tx.Reference()
		codes := slices.Sorted(maps.Keys(current.SelectionValues))
		heads, err := registry.DictionaryRevisions(ctx, codes)
		if err != nil {
			return err
		}
		update.version = current.Version
		update.heads = heads
		changed := s.changedDictionaries(codes, heads)
		if len(changed) == 0 {
			return nil
		}

		values, err := observeChangedValues(ctx, registry, changed, heads)
		if err != nil {
			return err
		}
		for code, pool := range values {
			if !reflect.DeepEqual(current.SelectionValues[code], pool) {
				if update.values == nil {
					update.values = make(map[string]taxonomy.DictionaryValues)
				}
				update.values[code] = pool
			}
		}
		return nil
	})
	return update, err
}

func (s *SelectionRefresh) changedDictionaries(codes []string, heads map[string]int64) []string {
	var changed []string
	for _, code := range codes {
		revision, seen := s.revisions[code]
		if !seen || revision != heads[code] {
			changed = append(changed, code)
		}
	}
	return changed
}

func observeChangedValues(
	ctx context.Context,
	registry ports.TaxonomyDictionaries,
	codes []string,
	heads map[string]int64,
) (map[string]taxonomy.DictionaryValues, error) {
	values := make(map[string]taxonomy.DictionaryValues, len(codes))
	for _, code := range codes {
		pool, err := loadSelectionValues(ctx, registry, code)
		if err != nil {
			return nil, err
		}
		values[code] = pool
	}
	// Записи и алиасы читаются отдельно. Смена ревизии требует повторного чтения,
	// чтобы не публиковать значения и алиасы из двух разных состояний.
	after, err := registry.DictionaryRevisions(ctx, codes)
	if err != nil {
		return nil, err
	}
	for _, code := range codes {
		if after[code] != heads[code] {
			delete(values, code)
			delete(heads, code)
		}
	}
	return values, nil
}

func (s *SelectionRefresh) publish(ctx context.Context, update selectionUpdate) (bool, error) {
	applied := false
	err := s.service.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		repository := tx.Taxonomy()
		if err := repository.Lock(ctx); err != nil {
			return err
		}
		current, err := repository.Current(ctx)
		if err != nil || current.Version != update.version {
			return err
		}
		if current.Version >= taxonomy.MaxTaxonomyVersion {
			return taxonomy.ErrConflict
		}

		next := current
		next.SelectionValues = maps.Clone(current.SelectionValues)
		maps.Copy(next.SelectionValues, update.values)
		next.Version++
		if err := repository.Save(ctx, next, current.Version); err != nil {
			return err
		}
		applied = true
		categories := taxonomy.AffectedCategories(current, next)
		if len(categories) > 0 {
			return repository.QueueRevalidation(ctx, next.Version, categories)
		}
		return nil
	})
	return applied, err
}
