package postgres

import (
	"context"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
)

const (
	claimCartAdd = `
		WITH expired AS (
			DELETE FROM cart_add_operations
			WHERE (buyer_id, key) IN (
				SELECT buyer_id, key FROM cart_add_operations
				WHERE expires_at < $7::timestamptz - interval '1 minute'
				ORDER BY expires_at LIMIT 32
				FOR UPDATE SKIP LOCKED
			)
		)
		INSERT INTO cart_add_operations (buyer_id, key, offer_id, quantity, owner, lease_until, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $8)
		ON CONFLICT (buyer_id, key) DO UPDATE
		SET owner = EXCLUDED.owner,
			lease_until = EXCLUDED.lease_until
		WHERE cart_add_operations.offer_id = EXCLUDED.offer_id AND cart_add_operations.quantity = EXCLUDED.quantity
			AND NOT cart_add_operations.completed AND cart_add_operations.lease_until <= $7`

	selectCartAdd = `
		SELECT offer_id, quantity, completed FROM cart_add_operations WHERE buyer_id = $1 AND key = $2`

	completeCartAdd = `
		UPDATE cart_add_operations
		SET completed = true
		WHERE buyer_id = $1 AND key = $2 AND owner = $3 AND NOT completed`

	releaseCartAdd = `
		UPDATE cart_add_operations
		SET lease_until = '-infinity'
		WHERE buyer_id = $1 AND key = $2 AND owner = $3 AND NOT completed`
)

// ClaimAdd захватывает новое или оставленное добавление и определяет результат повтора.
func (r *Carts) ClaimAdd(ctx context.Context, operation cart.AddOperation) (bool, error) {
	affected, err := execSQL(
		ctx,
		r.db,
		claimCartAdd,
		operation.BuyerID,
		operation.Key,
		operation.Offer.String(),
		operation.Quantity,
		operation.Owner,
		operation.LeaseUntil,
		operation.Now,
		operation.ExpiresAt,
	)
	if err != nil {
		return false, fmt.Errorf("ключ добавления в корзину: %w", err)
	}
	if affected != 0 {
		return false, nil
	}
	var offer string
	var quantity int
	var completed bool
	row := rawSQL(ctx, r.db, selectCartAdd, operation.BuyerID, operation.Key).Row()
	if err := row.Scan(&offer, &quantity, &completed); err != nil {
		return false, err
	}
	if offer != operation.Offer.String() || quantity != operation.Quantity {
		return false, cart.ErrAddKeyReused
	}
	if !completed {
		return false, cart.ErrAddInProgress
	}
	return true, nil
}

// CompleteAdd фиксируется в одной транзакции с сохранением корзины.
func (r *Carts) CompleteAdd(ctx context.Context, operation cart.AddOperation) error {
	affected, err := execSQL(ctx, r.db, completeCartAdd, operation.BuyerID, operation.Key, operation.Owner)
	if err != nil {
		return err
	}
	if affected == 0 {
		return cart.ErrAddInProgress
	}
	return nil
}

// ReleaseAdd освобождает аренду, сохраняя привязку ключа к телу запроса.
func (r *Carts) ReleaseAdd(ctx context.Context, operation cart.AddOperation) error {
	_, err := execSQL(ctx, r.db, releaseCartAdd, operation.BuyerID, operation.Key, operation.Owner)
	return err
}
