package cart

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// AddOperation резервирует ключ покупателя до обращения в магазин.
type AddOperation struct {
	BuyerID    ids.UUID
	Key        string
	Offer      ids.OfferID
	Quantity   int
	Owner      ids.UUID
	LeaseUntil time.Time
	ExpiresAt  time.Time
	Now        time.Time
}

// Repository — корзины покупателей.
type Repository interface {
	// ByBuyer — корзина покупателя; нет — ErrNotFound.
	ByBuyer(ctx context.Context, buyerID ids.UUID) (*Cart, error)
	// Save заводит корзину или обновляет условно по исходной версии; не совпала — ErrConflict.
	Save(ctx context.Context, cart *Cart) error
	// ClaimAdd возвращает true при повторе уже выполненного добавления.
	ClaimAdd(ctx context.Context, operation AddOperation) (replayed bool, err error)
	CompleteAdd(ctx context.Context, operation AddOperation) error
	ReleaseAdd(ctx context.Context, operation AddOperation) error
	// DeleteExpired удаляет корзины, просроченные до before, пачкой не больше limit.
	DeleteExpired(ctx context.Context, before time.Time, limit int) (int64, error)
}
