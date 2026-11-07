package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	selectCart = `
		SELECT id, buyer_id, version, created_at, updated_at, expires_at, promos
		FROM carts
		WHERE buyer_id = $1`

	selectCartLines = `
		SELECT tenant_id, product_id, name, quantity, price, issue, added_at, checked_at, discounts
		FROM cart_lines
		WHERE cart_id = $1
		ORDER BY position`

	insertCart = `
		INSERT INTO carts (id, buyer_id, version, created_at, updated_at, expires_at, promos)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	updateCart = `
		UPDATE carts
		SET version = $3,
			updated_at = $4,
			expires_at = $5,
			promos = $6
		WHERE id = $1 AND version = $2`

	deleteCartLines = `DELETE FROM cart_lines WHERE cart_id = $1`

	insertCartLine = `
		INSERT INTO cart_lines (
			cart_id, tenant_id, product_id, position, name, quantity, price, issue, added_at, checked_at, discounts
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	deleteExpiredCarts = `
		DELETE FROM carts
		WHERE id IN (SELECT id FROM carts WHERE expires_at < $1 LIMIT $2)`
)

// Carts — корзины в PostgreSQL.
type Carts struct {
	q querier
}

// ByBuyer — корзина покупателя со строками по порядку добавления.
func (r *Carts) ByBuyer(ctx context.Context, buyerID ids.UUID) (*cart.Cart, error) {
	var (
		state       cart.State
		id, buyerFK string
		promos      []byte
	)

	err := r.q.QueryRow(ctx, selectCart, string(buyerID)).
		Scan(&id, &buyerFK, &state.Version, &state.CreatedAt, &state.UpdatedAt, &state.ExpiresAt, &promos)
	if notFound(err) {
		return nil, cart.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("корзина: %w", err)
	}

	if err := json.Unmarshal(promos, &state.Promos); err != nil {
		return nil, err
	}
	state.ID = ids.UUID(id)
	state.BuyerID = ids.UUID(buyerFK)

	if state.Lines, err = r.lines(ctx, id); err != nil {
		return nil, err
	}

	return cart.Restore(state), nil
}

// Save заводит корзину или обновляет её условно по версии; строки
// переписываются целиком — их не больше сотни.
func (r *Carts) Save(ctx context.Context, c *cart.Cart) error {
	s := c.State()

	if c.OriginVersion() == 0 {
		if err := r.insert(ctx, s); err != nil {
			return err
		}
	} else if err := r.update(ctx, s, c.OriginVersion()); err != nil {
		return err
	}

	for position, line := range s.Lines {
		discounts, err := jsonValue(line.Discounts, "[]")
		if err != nil {
			return err
		}
		_, err = r.q.Exec(
			ctx,
			insertCartLine,
			string(s.ID),
			string(line.Offer.Tenant),
			string(line.Offer.Product),
			position,
			line.Name,
			line.Quantity,
			line.Price.Kopecks(),
			string(line.Issue),
			line.AddedAt,
			line.CheckedAt,
			discounts,
		)
		if err != nil {
			return fmt.Errorf("строка корзины %s: %w", s.ID, err)
		}
	}

	c.MarkSaved()

	return nil
}

// DeleteExpired удаляет просроченные корзины пачкой.
func (r *Carts) DeleteExpired(ctx context.Context, before time.Time, limit int) (int64, error) {
	tag, err := r.q.Exec(ctx, deleteExpiredCarts, before, limit)
	if err != nil {
		return 0, fmt.Errorf("чистка корзин: %w", err)
	}

	return tag.RowsAffected(), nil
}

func (r *Carts) insert(ctx context.Context, s cart.State) error {
	promos, err := jsonValue(s.Promos, "{}")
	if err != nil {
		return err
	}
	_, err = r.q.Exec(
		ctx,
		insertCart,
		string(s.ID),
		string(s.BuyerID),
		s.Version,
		s.CreatedAt,
		s.UpdatedAt,
		s.ExpiresAt,
		promos,
	)

	switch {
	case isUnique(err, ""):
		return cart.ErrConflict
	case err != nil:
		return fmt.Errorf("новая корзина: %w", err)
	default:
		return nil
	}
}

// update обновляет корзину условно по версии и стирает прежние строки.
func (r *Carts) update(ctx context.Context, s cart.State, origin int64) error {
	promos, err := jsonValue(s.Promos, "{}")
	if err != nil {
		return err
	}
	tag, err := r.q.Exec(
		ctx,
		updateCart,
		string(s.ID),
		origin,
		s.Version,
		s.UpdatedAt,
		s.ExpiresAt,
		promos,
	)
	if err != nil {
		return fmt.Errorf("корзина %s: %w", s.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return cart.ErrConflict
	}

	if _, err := r.q.Exec(ctx, deleteCartLines, string(s.ID)); err != nil {
		return fmt.Errorf("строки корзины %s: %w", s.ID, err)
	}

	return nil
}

func (r *Carts) lines(ctx context.Context, cartID string) ([]cart.Line, error) {
	rows, err := r.q.Query(ctx, selectCartLines, cartID)
	if err != nil {
		return nil, fmt.Errorf("строки корзины: %w", err)
	}
	defer rows.Close()

	var lines []cart.Line

	for rows.Next() {
		var (
			line            cart.Line
			tenant, product string
			price           int64
			issue           string
			discounts       []byte
		)

		err := rows.Scan(
			&tenant,
			&product,
			&line.Name,
			&line.Quantity,
			&price,
			&issue,
			&line.AddedAt,
			&line.CheckedAt,
			&discounts,
		)
		if err != nil {
			return nil, fmt.Errorf("строки корзины: %w", err)
		}

		if err := json.Unmarshal(discounts, &line.Discounts); err != nil {
			return nil, err
		}
		line.Offer = ids.OfferID{Tenant: ids.TenantID(tenant), Product: ids.ProductID(product)}
		line.Price = money.FromKopecks(price)
		line.Issue = cart.Issue(issue)
		lines = append(lines, line)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("строки корзины: %w", err)
	}

	return lines, nil
}
