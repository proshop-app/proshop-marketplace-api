package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
)

func projectedProduct(
	ctx context.Context,
	tx ports.Tx,
	snapshot catalog.Snapshot,
	now time.Time,
) (*catalog.Product, *catalog.ProductState, error) {
	product, err := tx.Products().Get(ctx, snapshot.TenantID, snapshot.ProductID)
	if errors.Is(err, catalog.ErrProductNotFound) {
		product, err = catalog.NewProduct(snapshot, now)
		return product, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	previous := product.State()
	changed, err := product.Apply(snapshot, now)
	if err != nil || !changed {
		return nil, nil, err
	}
	return product, &previous, nil
}

func (p *Projector) validateProjection(
	ctx context.Context,
	tx ports.Tx,
	product *catalog.Product,
	previous *catalog.ProductState,
	now time.Time,
) (bool, error) {
	reuse, err := p.prepareProjectionReuse(ctx, tx, product, previous)
	if err != nil {
		return false, err
	}
	if reuse == nil {
		definition, err := tx.Taxonomy().Current(ctx)
		if err != nil {
			return false, err
		}
		product.Revalidate(definition, now)
	}
	if err := tx.Products().Save(ctx, product); err != nil {
		return false, err
	}
	if reuse != nil {
		return true, p.evaluator.ReuseProjection(ctx, tx, product, reuse, now)
	}
	if p.evaluator != nil {
		return false, p.evaluator.EvaluateProjection(ctx, tx, product, previous, now)
	}
	return false, nil
}

func (p *Projector) prepareProjectionReuse(
	ctx context.Context,
	tx ports.Tx,
	product *catalog.Product,
	previous *catalog.ProductState,
) (*ports.ProjectionReuse, error) {
	if p.evaluator == nil || previous == nil || previous.Deleted || !previous.Card.Publication.Published ||
		!catalog.SameValidationContent(*previous, product.State()) {
		return nil, nil
	}
	return p.evaluator.PrepareReuse(ctx, tx, *previous)
}
