package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

const listCheckouts = `
 SELECT id::text
 FROM checkouts c
 WHERE ($1 = '' OR buyer_id::text = $1) AND id::text > $2
  AND (NOT $3 OR status NOT IN ('draft', 'confirmed'))
  AND ($4::timestamptz IS NULL OR EXISTS (
   SELECT 1 FROM checkout_groups g
   WHERE g.checkout_id = c.id AND g.order_progress->>'Status' = 'awaiting_payment'
    AND (g.order_progress->>'AwaitingSince')::timestamptz <= $4
  ))
 AND ($6::timestamptz IS NULL OR order_context->>'CartConsumed' IS DISTINCT FROM 'true' OR EXISTS (
  SELECT 1 FROM checkout_groups g WHERE g.checkout_id = c.id AND (
   (g.order_progress->>'Status' IN ('creating_order', 'seller_unreachable')
    AND coalesce(g.order_progress->>'OrderID', '') = ''
    AND g.order_progress->>'CreateExhausted' IS DISTINCT FROM 'true'
    AND coalesce((g.order_progress->>'NextCreateAt')::timestamptz, '-infinity') <= $6)
   OR (g.order_progress->>'Status' IN ('cancelled', 'price_changed', 'delivery_unavailable')
    AND g.order_progress->>'CartReturned' IS DISTINCT FROM 'true'
    AND coalesce((g.order_progress->>'NextReturnAt')::timestamptz, '-infinity') <= $6)
   OR (g.order_progress->>'OrderID' <> ''
    AND g.order_progress->>'Status' NOT IN ('cancelled', 'returned')
    AND (g.order_progress->>'NextPollAt')::timestamptz <= $6)
  )
 ))
 ORDER BY id::text
 LIMIT $5`

type checkoutOrderContext struct {
	Contact      checkout.Contact
	ReturnURL    string
	CartConsumed bool
}

func encodeOrderContext(s checkout.State) string {
	raw, _ := json.Marshal(checkoutOrderContext{
		Contact:      s.Contact,
		ReturnURL:    s.ReturnURL,
		CartConsumed: s.CartConsumed,
	})
	return string(raw)
}

func encodeOrderProgress(g checkout.Group) string {
	g.Lines, g.Quote = nil, nil
	raw, _ := json.Marshal(g)
	return string(raw)
}

// List выбирает страницу покупок для кабинета, панели или фоновой саги.
func (r *Checkouts) List(ctx context.Context, f checkout.Filter) ([]*checkout.Checkout, error) {
	rows, err := r.q.Query(
		ctx,
		listCheckouts,
		string(f.BuyerID),
		string(f.AfterID),
		f.OrdersOnly,
		nullTime(f.AwaitingBefore),
		f.Limit,
		nullTime(f.WorkDueAt),
	)
	if err != nil {
		return nil, fmt.Errorf("список покупок: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]*checkout.Checkout, 0, len(ids))
	for _, id := range ids {
		c, err := r.one(ctx, `id::text = $1`, id)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, nil
}
