package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	lockTaxonomyProgress = `SELECT version, tenant_id, product_id, done, cards_version, cards_dirty
  FROM taxonomy_revalidation WHERE singleton FOR UPDATE`
	saveTaxonomyProgress = `UPDATE taxonomy_revalidation
  SET version = $1,
   tenant_id = $2,
   product_id = $3,
   done = $4,
   cards_version = $5,
   cards_dirty = $6
  WHERE singleton`
)

// LockProgress защищает курсор от параллельного прохода одной версии.
func (r *Taxonomy) LockProgress(ctx context.Context) (ports.TaxonomyProgress, error) {
	var p ports.TaxonomyProgress
	err := r.q.QueryRow(ctx, lockTaxonomyProgress).Scan(
		&p.Version,
		&p.TenantID,
		&p.ProductID,
		&p.Done,
		&p.CardsVersion,
		&p.CardsDirty,
	)
	return p, err
}

// SaveProgress вызывается под блокировкой головы и курсора.
func (r *Taxonomy) SaveProgress(ctx context.Context, p ports.TaxonomyProgress) error {
	_, err := r.q.Exec(
		ctx,
		saveTaxonomyProgress,
		p.Version,
		p.TenantID,
		p.ProductID,
		p.Done,
		p.CardsVersion,
		p.CardsDirty,
	)
	return err
}
