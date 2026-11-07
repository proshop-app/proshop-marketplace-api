package ports

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CartVerifier перепроверяет корзину после изменения условий магазина.
type CartVerifier interface {
	Verify(context.Context, ids.UUID) (cart.State, error)
}
