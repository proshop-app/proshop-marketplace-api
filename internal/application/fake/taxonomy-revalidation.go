package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func (r taxonomies) QueueRevalidation(_ context.Context, version int64, categories []string) error {
	for _, category := range categories {
		r.d.revalidations = append(r.d.revalidations, taxonomy.Revalidation{
			Version:  version,
			Category: category,
		})
	}
	return nil
}

func (r taxonomies) NextRevalidation(context.Context) (*taxonomy.Revalidation, error) {
	for _, job := range r.d.revalidations {
		if !job.Done {
			return &job, nil
		}
	}
	return nil, nil
}

func (r taxonomies) AdvanceRevalidation(_ context.Context, job taxonomy.Revalidation) error {
	for i, existing := range r.d.revalidations {
		if existing.Version == job.Version && existing.Category == job.Category {
			r.d.revalidations[i] = job
			return nil
		}
	}
	return taxonomy.ErrConflict
}

func (r products) RevalidationPage(
	_ context.Context,
	job taxonomy.Revalidation,
	limit int,
) ([]*catalog.Product, error) {
	keys := offers(r).keys(func(key productKey) bool {
		state := r.d.products[key]
		card := state.Card.Marketplace
		return card != nil && card.CategoryID == job.Category &&
			(string(key.tenant) > job.TenantCursor ||
				string(key.tenant) == job.TenantCursor && string(key.product) > job.ProductCursor)
	})
	items := []*catalog.Product{}
	for _, key := range keys[:min(limit, len(keys))] {
		items = append(items, catalog.RestoreProduct(r.d.products[key]))
	}
	return items, nil
}
