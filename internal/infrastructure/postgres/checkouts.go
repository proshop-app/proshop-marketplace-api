package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// lineJSON — позиция снимка в колонке jsonb; цена — копейки.
type lineJSON struct {
	ProductID string `json:"productId"`
	Name      string `json:"name"`
	Quantity  int    `json:"quantity"`
	Price     int64  `json:"price"`
}

// selectionJSON — выбранная доставка в колонке jsonb.
type selectionJSON struct {
	City    string `json:"city"`
	Address string `json:"address"`
}

// Checkouts — покупки в PostgreSQL.
type Checkouts struct {
	q querier
}

const checkoutColumns = `id, buyer_id, idempotency_key, status, carrier, pickup_point_id, delivery, goods_total, delivery_total,
	total, version, created_at, updated_at, confirmed_at`

func (r *Checkouts) one(ctx context.Context, where string, args ...any) (*checkout.Checkout, error) {
	var (
		state               checkout.State
		id, buyerID, status string
		carrier, pointID    *string
		selection           []byte
		goods, deliv, total int64
		confirmed           *time.Time
	)

	err := r.q.QueryRow(ctx, `SELECT `+checkoutColumns+` FROM checkouts WHERE `+where, args...).Scan(
		&id, &buyerID, &state.IdempotencyKey, &status, &carrier, &pointID, &selection, &goods, &deliv, &total,
		&state.Version, &state.CreatedAt, &state.UpdatedAt, &confirmed)
	if notFound(err) {
		return nil, checkout.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("покупка: %w", err)
	}

	state.ID, state.BuyerID, state.Status = ids.UUID(id), ids.UUID(buyerID), checkout.Status(status)
	state.GoodsTotal, state.DeliveryTotal, state.Total = money.FromKopecks(goods), money.FromKopecks(deliv), money.FromKopecks(total)
	if confirmed != nil {
		state.ConfirmedAt = *confirmed
	}

	if carrier != nil {
		var doc selectionJSON
		if len(selection) > 0 {
			if err := json.Unmarshal(selection, &doc); err != nil {
				return nil, fmt.Errorf("доставка покупки %s: %w", id, err)
			}
		}

		state.Delivery = &checkout.Selection{Carrier: delivery.Carrier(*carrier), PickupPointCode: *pointID, City: doc.City, Address: doc.Address}
	}

	if state.Groups, err = r.groups(ctx, id); err != nil {
		return nil, err
	}

	return checkout.Restore(state), nil
}

func (r *Checkouts) groups(ctx context.Context, checkoutID string) ([]checkout.Group, error) {
	rows, err := r.q.Query(ctx, `SELECT tenant_id, external_checkout_id, lines, goods, quote_status, quote_reason, delivery_cost,
		subsidy, buyer_delivery, min_days, max_days, quote_expires_at
		FROM checkout_groups WHERE checkout_id = $1 ORDER BY tenant_id`, checkoutID)
	if err != nil {
		return nil, fmt.Errorf("группы покупки: %w", err)
	}
	defer rows.Close()

	var groups []checkout.Group

	for rows.Next() {
		var (
			group                        checkout.Group
			tenant                       string
			lines                        []byte
			goods                        int64
			quoteStatus, reason          *string
			cost, subsidy, buyerDelivery *int64
			minDays, maxDays             *int
			expires                      *time.Time
		)

		if err := rows.Scan(&tenant, &group.ExternalCheckoutID, &lines, &goods, &quoteStatus, &reason, &cost, &subsidy,
			&buyerDelivery, &minDays, &maxDays, &expires); err != nil {
			return nil, fmt.Errorf("группы покупки: %w", err)
		}

		group.TenantID, group.Goods = ids.TenantID(tenant), money.FromKopecks(goods)

		var docs []lineJSON
		if err := json.Unmarshal(lines, &docs); err != nil {
			return nil, fmt.Errorf("позиции покупки %s: %w", checkoutID, err)
		}

		for _, doc := range docs {
			group.Lines = append(group.Lines, checkout.Line{
				Offer: ids.OfferID{Tenant: group.TenantID, Product: ids.ProductID(doc.ProductID)},
				Name:  doc.Name, Quantity: doc.Quantity, Price: money.FromKopecks(doc.Price),
			})
		}

		if quoteStatus != nil {
			quote := delivery.Quote{Status: delivery.QuoteStatus(*quoteStatus), ExpiresAt: *expires}
			if reason != nil {
				quote.Reason = *reason
			}
			if cost != nil {
				quote.Cost, quote.Subsidy, quote.BuyerCost = money.FromKopecks(*cost), money.FromKopecks(*subsidy), money.FromKopecks(*buyerDelivery)
				quote.MinDays, quote.MaxDays = *minDays, *maxDays
			}

			group.Quote = &quote
		}

		groups = append(groups, group)
	}

	return groups, rows.Err()
}

// Get — покупка.
func (r *Checkouts) Get(ctx context.Context, id ids.UUID) (*checkout.Checkout, error) {
	return r.one(ctx, `id = $1`, string(id))
}

// ByKey — покупка покупателя по ключу идемпотентности.
func (r *Checkouts) ByKey(ctx context.Context, buyerID ids.UUID, key string) (*checkout.Checkout, error) {
	return r.one(ctx, `buyer_id = $1 AND idempotency_key = $2`, string(buyerID), key)
}

// Add сохраняет новую покупку с группами.
func (r *Checkouts) Add(ctx context.Context, c *checkout.Checkout) error {
	s := c.State()
	carrier, point, selection, err := selectionArgs(s.Delivery)
	if err != nil {
		return err
	}

	_, err = r.q.Exec(ctx, `INSERT INTO checkouts (`+checkoutColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		string(s.ID), string(s.BuyerID), s.IdempotencyKey, string(s.Status), carrier, point, selection,
		s.GoodsTotal.Kopecks(), s.DeliveryTotal.Kopecks(), s.Total.Kopecks(), s.Version, s.CreatedAt, s.UpdatedAt, nullTime(s.ConfirmedAt))
	if isUnique(err, "checkouts_buyer_key_key") {
		return checkout.ErrDuplicateKey
	}
	if err != nil {
		return fmt.Errorf("новая покупка: %w", err)
	}

	for _, group := range s.Groups {
		args, err := groupArgs(group)
		if err != nil {
			return err
		}

		if _, err := r.q.Exec(ctx, `INSERT INTO checkout_groups (checkout_id, tenant_id, external_checkout_id, lines, goods,
			quote_status, quote_reason, delivery_cost, subsidy, buyer_delivery, min_days, max_days, quote_expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`, append([]any{string(s.ID)}, args...)...); err != nil {
			return fmt.Errorf("группа покупки %s: %w", s.ID, err)
		}
	}

	c.MarkSaved()

	return nil
}

// Update сохраняет условно по исходным статусу и версии; группы переписываются.
func (r *Checkouts) Update(ctx context.Context, c *checkout.Checkout) error {
	s := c.State()
	status, version := c.Origin()

	carrier, point, selection, err := selectionArgs(s.Delivery)
	if err != nil {
		return err
	}

	tag, err := r.q.Exec(ctx, `UPDATE checkouts
		SET status = $4, carrier = $5, pickup_point_id = $6, delivery = $7, goods_total = $8, delivery_total = $9, total = $10,
			version = $11, updated_at = $12, confirmed_at = $13
		WHERE id = $1 AND status = $2 AND version = $3`,
		string(s.ID), string(status), version, string(s.Status), carrier, point, selection,
		s.GoodsTotal.Kopecks(), s.DeliveryTotal.Kopecks(), s.Total.Kopecks(), s.Version, s.UpdatedAt, nullTime(s.ConfirmedAt))
	if err != nil {
		return fmt.Errorf("покупка %s: %w", s.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return checkout.ErrConflict
	}

	for _, group := range s.Groups {
		args, err := groupArgs(group)
		if err != nil {
			return err
		}

		if _, err := r.q.Exec(ctx, `UPDATE checkout_groups SET external_checkout_id = $3, lines = $4, goods = $5, quote_status = $6,
			quote_reason = $7, delivery_cost = $8, subsidy = $9, buyer_delivery = $10, min_days = $11, max_days = $12, quote_expires_at = $13
			WHERE checkout_id = $1 AND tenant_id = $2`, append([]any{string(s.ID)}, args...)...); err != nil {
			return fmt.Errorf("группа покупки %s: %w", s.ID, err)
		}
	}

	c.MarkSaved()

	return nil
}

func selectionArgs(selection *checkout.Selection) (any, any, any, error) {
	if selection == nil {
		return nil, nil, nil, nil
	}

	encoded, err := json.Marshal(selectionJSON{City: selection.City, Address: selection.Address})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("доставка покупки: %w", err)
	}

	return string(selection.Carrier), selection.PickupPointCode, string(encoded), nil
}

func groupArgs(group checkout.Group) ([]any, error) {
	docs := make([]lineJSON, 0, len(group.Lines))
	for _, line := range group.Lines {
		docs = append(docs, lineJSON{ProductID: string(line.Offer.Product), Name: line.Name, Quantity: line.Quantity, Price: line.Price.Kopecks()})
	}

	lines, err := json.Marshal(docs)
	if err != nil {
		return nil, fmt.Errorf("позиции покупки: %w", err)
	}

	args := []any{string(group.TenantID), group.ExternalCheckoutID, string(lines), group.Goods.Kopecks()}

	q := group.Quote
	if q == nil {
		return append(args, nil, nil, nil, nil, nil, nil, nil, nil), nil
	}

	var reason any
	if q.Reason != "" {
		reason = q.Reason
	}

	if q.Status != delivery.QuoteAvailable {
		return append(args, string(q.Status), reason, nil, nil, nil, nil, nil, q.ExpiresAt), nil
	}

	return append(args, string(q.Status), reason, q.Cost.Kopecks(), q.Subsidy.Kopecks(), q.BuyerCost.Kopecks(),
		q.MinDays, q.MaxDays, q.ExpiresAt), nil
}
