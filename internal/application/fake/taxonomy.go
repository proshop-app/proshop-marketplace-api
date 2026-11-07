package fake

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type taxonomies struct{ d *data }

func cloneTaxonomy(d taxonomy.Definition) taxonomy.Definition {
	raw, _ := json.Marshal(d)
	var out taxonomy.Definition
	_ = json.Unmarshal(raw, &out)
	return out
}

func (r taxonomies) Current(context.Context) (taxonomy.Definition, error) {
	return cloneTaxonomy(r.d.taxonomy), nil
}

func (r taxonomies) Lock(context.Context) error { return nil }

func (r taxonomies) Save(_ context.Context, d taxonomy.Definition, expected int64) error {
	if r.d.taxonomy.Version != expected {
		return taxonomy.ErrConflict
	}
	r.d.taxonomy = cloneTaxonomy(d)
	r.d.taxonomyVersions[d.Version] = cloneTaxonomy(d)
	return nil
}

// SeedTaxonomy задаёт схему для тестов сценариев.
func (s *Store) SeedTaxonomy(d taxonomy.Definition) {
	s.read(func(data *data) {
		data.taxonomy = cloneTaxonomy(d)
		data.taxonomyVersions[d.Version] = cloneTaxonomy(d)
	})
}

func (r products) Page(
	_ context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
	limit int,
) ([]*catalog.Product, error) {
	r.d.productPageCalls++
	o := offers(r)
	keys := o.keys(func(key productKey) bool {
		return after(key, ports.OfferKey{TenantID: tenant, ProductID: product})
	})
	out := []*catalog.Product{}
	for _, key := range keys {
		if len(out) == limit {
			break
		}
		out = append(out, catalog.RestoreProduct(r.d.products[key]))
	}
	return out, nil
}

func (r taxonomies) LockProgress(context.Context) (ports.TaxonomyProgress, error) {
	return r.d.revalidation, nil
}

func (r taxonomies) SaveProgress(_ context.Context, p ports.TaxonomyProgress) error {
	r.d.revalidation = p
	return nil
}

// ProductPageCalls считает только зафиксированные чтения страниц каталога.
func (s *Store) ProductPageCalls() int {
	var count int
	s.read(func(d *data) { count = d.productPageCalls })
	return count
}

func (r taxonomies) Version(_ context.Context, version int64) (taxonomy.Definition, error) {
	if version == 0 {
		return taxonomy.Definition{}, nil
	}
	d, ok := r.d.taxonomyVersions[version]
	if !ok {
		return taxonomy.Definition{}, fmt.Errorf("снимок таксономии %d: %w", version, taxonomy.ErrConflict)
	}
	return cloneTaxonomy(d), nil
}
