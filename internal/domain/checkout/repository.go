package checkout

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Filter — курсор по ID, владелец и окно ожидания для панели оператора.
type Filter struct {
	BuyerID        ids.UUID
	AfterID        ids.UUID
	Limit          int
	AwaitingBefore time.Time
	OrdersOnly     bool
	WorkDueAt      time.Time
}

// Repository — покупки.
type Repository interface {
	List(context.Context, Filter) ([]*Checkout, error)
	// Get — покупка; нет — ErrNotFound.
	Get(ctx context.Context, id ids.UUID) (*Checkout, error)
	// ByKey — покупка покупателя по ключу идемпотентности; нет — ErrNotFound.
	ByKey(ctx context.Context, buyerID ids.UUID, key string) (*Checkout, error)
	// Add сохраняет новую покупку; ключ занят — ErrDuplicateKey.
	Add(ctx context.Context, checkout *Checkout) error
	// Update сохраняет условно по исходным статусу и версии; не совпали — ErrConflict.
	Update(ctx context.Context, checkout *Checkout) error
}
