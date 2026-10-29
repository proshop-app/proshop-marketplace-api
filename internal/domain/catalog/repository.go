package catalog

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ProductRepository — проекция товаров магазинов.
type ProductRepository interface {
	Page(ctx context.Context, tenant ids.TenantID, product ids.ProductID, limit int) ([]*Product, error)
	// Get — запись; нет — ErrProductNotFound.
	Get(ctx context.Context, tenantID ids.TenantID, productID ids.ProductID) (*Product, error)
	// Save заводит или условно по исходной версии обновляет запись; не совпала — ErrConflict.
	Save(ctx context.Context, product *Product) error
}

// SignalsRepository — проекция остатков и цен.
type SignalsRepository interface {
	// Get — сигналы; нет — ErrSignalsNotFound.
	Get(ctx context.Context, tenantID ids.TenantID, productID ids.ProductID) (*Signals, error)
	// Save заводит или условно по исходной версии обновляет сигналы; не совпала — ErrConflict.
	Save(ctx context.Context, signals *Signals) error
}
