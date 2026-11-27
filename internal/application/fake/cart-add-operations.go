package fake

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type cartAddKey struct {
	buyer ids.UUID
	key   string
}

type cartAddRecord struct {
	operation cart.AddOperation
	completed bool
}

func (r carts) ClaimAdd(_ context.Context, operation cart.AddOperation) (bool, error) {
	key := cartAddKey{operation.BuyerID, operation.Key}
	record, found := r.d.cartAdds[key]
	if found {
		if record.operation.Offer != operation.Offer || record.operation.Quantity != operation.Quantity {
			return false, cart.ErrAddKeyReused
		}
		if record.completed {
			return true, nil
		}
		if record.operation.LeaseUntil.After(operation.Now) {
			return false, cart.ErrAddInProgress
		}
	}
	r.d.cartAdds[key] = cartAddRecord{operation: operation}
	return false, nil
}

func (r carts) CompleteAdd(_ context.Context, operation cart.AddOperation) error {
	key := cartAddKey{operation.BuyerID, operation.Key}
	record := r.d.cartAdds[key]
	if record.operation.Owner != operation.Owner || record.completed {
		return cart.ErrAddInProgress
	}
	record.completed = true
	r.d.cartAdds[key] = record
	return nil
}

func (r carts) ReleaseAdd(_ context.Context, operation cart.AddOperation) error {
	key := cartAddKey{operation.BuyerID, operation.Key}
	record := r.d.cartAdds[key]
	if record.operation.Owner == operation.Owner && !record.completed {
		record.operation.LeaseUntil = time.Time{}
		r.d.cartAdds[key] = record
	}
	return nil
}
