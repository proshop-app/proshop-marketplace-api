package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CartVerifier позволяет проверить, что сбой вторичной сверки не подменяет результат покупки.
type CartVerifier struct {
	ports.CartVerifier
	Err   error
	Calls int
}

// Verify считает вызовы и отдаёт заданную ошибку, иначе — ответ встроенной реализации.
func (v *CartVerifier) Verify(ctx context.Context, buyerID ids.UUID) (cart.State, error) {
	v.Calls++
	if v.Err != nil {
		return cart.State{}, v.Err
	}
	return v.CartVerifier.Verify(ctx, buyerID)
}
