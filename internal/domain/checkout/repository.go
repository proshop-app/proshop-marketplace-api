package checkout

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Repository — покупки.
type Repository interface {
	// Get — покупка; нет — ErrNotFound.
	Get(ctx context.Context, id ids.UUID) (*Checkout, error)
	// ByKey — покупка покупателя по ключу идемпотентности; нет — ErrNotFound.
	ByKey(ctx context.Context, buyerID ids.UUID, key string) (*Checkout, error)
	// Add сохраняет новую покупку; ключ занят — ErrDuplicateKey.
	Add(ctx context.Context, checkout *Checkout) error
	// Update сохраняет условно по исходным статусу и версии; не совпали — ErrConflict.
	Update(ctx context.Context, checkout *Checkout) error
}
