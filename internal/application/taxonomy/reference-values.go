package taxonomy

import (
	"context"
	"slices"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

var errReferenceValues = errs.New(errs.Unavailable, "reference.values_unavailable", "значения схемы недоступны")

// ReferenceUnitOfWork подменяет только чтение значений схем. PostgreSQL
// хранит дерево и его историю; общие enum всегда читает владелец platform.
type ReferenceUnitOfWork struct {
	base   ports.UnitOfWork
	values ports.ReferenceValues
}

type referenceTx struct {
	ports.Tx
	values ports.ReferenceValues
}

type referenceTaxonomy struct {
	taxonomy.Repository
	values ports.ReferenceValues
}

// WithReferenceValues не делает запросов при старте и не меняет записи PG.
func WithReferenceValues(base ports.UnitOfWork, values ports.ReferenceValues) *ReferenceUnitOfWork {
	return &ReferenceUnitOfWork{base: base, values: values}
}

// Do сохраняет одну исходную транзакцию для всех остальных репозиториев.
func (u *ReferenceUnitOfWork) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return u.base.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return fn(ctx, referenceTx{Tx: tx, values: u.values})
	})
}

func (t referenceTx) Taxonomy() taxonomy.Repository {
	return referenceTaxonomy{Repository: t.Tx.Taxonomy(), values: t.values}
}

func (r referenceTaxonomy) Current(ctx context.Context) (taxonomy.Definition, error) {
	d, err := r.Repository.Current(ctx)
	if err != nil {
		return d, err
	}
	return hydrate(ctx, d, r.values)
}

// Для поставки и revalidation подменяется и входящий снимок: его прежние
// values — производная, а не право откатить общий справочник.
func (s *Service) hydrated(ctx context.Context, d taxonomy.Definition) (taxonomy.Definition, error) {
	if uow, ok := s.uow.(*ReferenceUnitOfWork); ok {
		return hydrate(ctx, d, uow.values)
	}
	return d, nil
}

func hydrate(ctx context.Context, d taxonomy.Definition, source ports.ReferenceValues) (taxonomy.Definition, error) {
	values := map[string][]ports.ReferenceValue{}
	for code, field := range d.Fields() {
		if field.Type != "enum" {
			continue
		}
		if source == nil {
			return taxonomy.Definition{}, errReferenceValues
		}
		items, err := source.Values(ctx, code)
		if err != nil {
			return taxonomy.Definition{}, err
		}
		if len(items) == 0 {
			return taxonomy.Definition{}, errReferenceValues
		}
		items = slices.Clone(items)
		sort.SliceStable(items, func(i, j int) bool { return items[i].Order < items[j].Order })
		values[code] = items
	}
	d.Categories = hydratedCategories(d.Categories, values)
	return d, nil
}

func hydratedCategories(
	categories []taxonomy.Category,
	values map[string][]ports.ReferenceValue,
) []taxonomy.Category {
	result := slices.Clone(categories)
	for i := range result {
		result[i].Attributes = slices.Clone(result[i].Attributes)
		for j := range result[i].Attributes {
			field := &result[i].Attributes[j]
			items, found := values[field.Code]
			if !found {
				continue
			}
			field.Values = make([]string, 0, len(items))
			field.ValuesMeta = map[string]taxonomy.ValueMeta{}
			for _, item := range items {
				field.Values = append(field.Values, item.Value)
				if item.Meta != (taxonomy.ValueMeta{}) {
					field.ValuesMeta[item.Value] = item.Meta
				}
			}
		}
		result[i].Children = hydratedCategories(result[i].Children, values)
	}
	return result
}
