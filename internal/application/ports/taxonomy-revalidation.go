package ports

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// TaxonomyProgress фиксируется вместе с пачкой; новая версия начинает проход с начала.
type TaxonomyProgress struct {
	Version      int64
	CardsVersion int64
	CardsDirty   bool
	TenantID     ids.TenantID
	ProductID    ids.ProductID
	Done         bool
}

// TaxonomyRevalidation сериализует обработчиков, в том числе после истечения аренды outbox.
// Порядок блокировок: сначала Taxonomy.Current/Lock, затем LockProgress.
type TaxonomyRevalidation interface {
	LockProgress(context.Context) (TaxonomyProgress, error)
	SaveProgress(context.Context, TaxonomyProgress) error
}
