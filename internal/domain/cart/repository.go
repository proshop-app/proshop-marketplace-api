package cart

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Repository — корзины покупателей.
type Repository interface {
	// ByBuyer — корзина покупателя; нет — ErrNotFound.
	ByBuyer(ctx context.Context, buyerID ids.UUID) (*Cart, error)
	// Save заводит корзину или обновляет условно по исходной версии; не совпала — ErrConflict.
	Save(ctx context.Context, cart *Cart) error
	// DeleteExpired удаляет корзины, просроченные до before, пачкой не больше limit.
	DeleteExpired(ctx context.Context, before time.Time, limit int) (int64, error)
}
