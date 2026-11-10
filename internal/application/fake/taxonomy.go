package fake

import (
	"context"
	"encoding/json"

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

func (r taxonomies) Version(_ context.Context, version int64) (taxonomy.Definition, error) {
	d, exists := r.d.taxonomyVersions[version]
	if !exists {
		return taxonomy.Definition{}, taxonomy.ErrInvalid.WithMessage("неизвестная версия таксономии")
	}
	return cloneTaxonomy(d), nil
}

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
