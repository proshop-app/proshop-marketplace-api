package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	selectCart = `
		SELECT c.id, c.buyer_id, c.version, c.created_at, c.updated_at, c.expires_at, c.promos,
			COALESCE((
				SELECT jsonb_agg(to_jsonb(l) - 'cart_id' ORDER BY l.position)
				FROM cart_lines l WHERE l.cart_id = c.id
			), '[]'::jsonb)
		FROM carts c
		WHERE c.buyer_id = $1`

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

	saveCartLineChanges = `
		WITH changes AS (
			SELECT * FROM jsonb_to_recordset($2::jsonb) AS x(
				tenant_id text, product_id text, position bigint, name text, quantity int,
				price bigint, issue text, added_at timestamptz, checked_at timestamptz, discounts jsonb, removed bool
			)
		), deleted AS (
			DELETE FROM cart_lines l USING changes c
			WHERE l.cart_id = $1 AND l.tenant_id = c.tenant_id AND l.product_id = c.product_id AND c.removed
		)
		INSERT INTO cart_lines (
			cart_id, tenant_id, product_id, position, name, quantity, price, issue, added_at, checked_at, discounts
		)
		SELECT $1, tenant_id, product_id, position, name, quantity, price, issue, added_at, checked_at,
			COALESCE(NULLIF(discounts, 'null'::jsonb), '[]'::jsonb)
		FROM changes WHERE NOT removed
		ON CONFLICT (cart_id, tenant_id, product_id) DO UPDATE
		SET position = EXCLUDED.position,
			name = EXCLUDED.name,
			quantity = EXCLUDED.quantity,
			price = EXCLUDED.price,
			issue = EXCLUDED.issue,
			added_at = EXCLUDED.added_at,
			checked_at = EXCLUDED.checked_at,
			discounts = EXCLUDED.discounts`

	deleteExpiredCarts = `
		DELETE FROM carts
		WHERE id IN (SELECT id FROM carts WHERE expires_at < $1 LIMIT $2)`
)

// Carts — корзины в PostgreSQL.
type Carts struct {
	db *gorm.DB
}

type cartLineChange struct {
	Tenant    ids.TenantID    `json:"tenant_id"`
	Product   ids.ProductID   `json:"product_id"`
	Position  int64           `json:"position"`
	Name      string          `json:"name"`
	Quantity  int             `json:"quantity"`
	Price     int64           `json:"price"`
	Issue     cart.Issue      `json:"issue"`
	AddedAt   time.Time       `json:"added_at"`
	CheckedAt time.Time       `json:"checked_at"`
	Discounts []cart.Discount `json:"discounts"`
	Removed   bool            `json:"removed"`
}

// ByBuyer — корзина покупателя со строками по порядку добавления.
func (r *Carts) ByBuyer(ctx context.Context, buyerID ids.UUID) (*cart.Cart, error) {
	var (
		state         cart.State
		id, buyerFK   string
		promos, lines []byte
	)

	// Версия и строки из одного снимка PostgreSQL, в том числе при повторе добавления.
	err := rawSQL(ctx, r.db, selectCart, string(buyerID)).Row().Scan(
		&id,
		&buyerFK,
		&state.Version,
		&state.CreatedAt,
		&state.UpdatedAt,
		&state.ExpiresAt,
		&promos,
		&lines,
	)
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

	var stored []cartLineChange
	if err := json.Unmarshal(lines, &stored); err != nil {
		return nil, fmt.Errorf("строки корзины: %w", err)
	}
	for _, line := range stored {
		state.Lines = append(state.Lines, cart.Line{
			Offer:     ids.OfferID{Tenant: line.Tenant, Product: line.Product},
			Position:  line.Position,
			Name:      line.Name,
			Quantity:  line.Quantity,
			Price:     money.FromKopecks(line.Price),
			Issue:     line.Issue,
			AddedAt:   line.AddedAt,
			CheckedAt: line.CheckedAt,
			Discounts: line.Discounts,
		})
	}

	return cart.Restore(state), nil
}

// Save обновляет корзину условно по версии и записывает только дельту строк.
func (r *Carts) Save(ctx context.Context, c *cart.Cart) error {
	s := c.State()

	if c.OriginVersion() == 0 {
		if err := r.insert(ctx, s); err != nil {
			return err
		}
	} else if err := r.update(ctx, s, c.OriginVersion()); err != nil {
		return err
	}

	if err := r.saveLines(ctx, c); err != nil {
		return err
	}
	c.MarkSaved()

	return nil
}

func (r *Carts) saveLines(ctx context.Context, c *cart.Cart) error {
	saved, removed := c.LineChanges()
	if len(saved)+len(removed) == 0 {
		return nil
	}
	changes := make([]cartLineChange, 0, len(saved)+len(removed))
	for _, line := range saved {
		changes = append(changes, cartLineChange{
			Tenant:    line.Offer.Tenant,
			Product:   line.Offer.Product,
			Position:  line.Position,
			Name:      line.Name,
			Quantity:  line.Quantity,
			Price:     line.Price.Kopecks(),
			Issue:     line.Issue,
			AddedAt:   line.AddedAt,
			CheckedAt: line.CheckedAt,
			Discounts: line.Discounts,
		})
	}
	for _, offer := range removed {
		changes = append(changes, cartLineChange{Tenant: offer.Tenant, Product: offer.Product, Removed: true})
	}
	payload, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	if _, err := execSQL(ctx, r.db, saveCartLineChanges, c.ID(), payload); err != nil {
		return fmt.Errorf("строки корзины %s: %w", c.ID(), err)
	}
	return nil
}

// DeleteExpired удаляет просроченные корзины пачкой.
func (r *Carts) DeleteExpired(ctx context.Context, before time.Time, limit int) (int64, error) {
	affected, err := execSQL(ctx, r.db, deleteExpiredCarts, before, limit)
	if err != nil {
		return 0, fmt.Errorf("чистка корзин: %w", err)
	}

	return affected, nil
}

func (r *Carts) insert(ctx context.Context, s cart.State) error {
	promos, err := jsonValue(s.Promos, "{}")
	if err != nil {
		return err
	}
	_, err = execSQL(
		ctx,
		r.db,
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

// update обновляет корзину условно по версии.
func (r *Carts) update(ctx context.Context, s cart.State, origin int64) error {
	promos, err := jsonValue(s.Promos, "{}")
	if err != nil {
		return err
	}
	affected, err := execSQL(
		ctx,
		r.db,
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
	if affected == 0 {
		return cart.ErrConflict
	}

	return nil
}
