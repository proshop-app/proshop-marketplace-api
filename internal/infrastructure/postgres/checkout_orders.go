package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

const (
	listCheckouts = `
 SELECT ` + checkoutColumns + `
 FROM checkouts c
 WHERE `

	listCheckoutFilters = `
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
   OR ((g.order_progress->>'Status' IN ('cancelled', 'price_changed', 'delivery_unavailable')
    OR (g.order_progress->>'CreateExhausted' = 'true' AND coalesce(g.order_progress->>'OrderID', '') = ''))
    AND g.order_progress->>'CartReturned' IS DISTINCT FROM 'true'
    AND coalesce((g.order_progress->>'NextReturnAt')::timestamptz, '-infinity') <= $6)
   OR (g.order_progress->>'OrderID' <> ''
    AND g.order_progress->>'Status' NOT IN ('cancelled', 'returned')
    AND (g.order_progress->>'NextPollAt')::timestamptz <= $6)
  )
 ))
 ORDER BY id
 LIMIT $5`

	// Отдельные формы сохраняют индексные условия и при generic prepared plan.
	listAllCheckouts        = listCheckouts + `$1::uuid IS NULL AND $2::uuid IS NULL` + listCheckoutFilters
	listBuyerCheckouts      = listCheckouts + `buyer_id = $1 AND $2::uuid IS NULL` + listCheckoutFilters
	listCheckoutsAfter      = listCheckouts + `$1::uuid IS NULL AND id > $2` + listCheckoutFilters
	listBuyerCheckoutsAfter = listCheckouts + `buyer_id = $1 AND id > $2` + listCheckoutFilters
)

type checkoutOrderContext struct {
	Contact      checkout.Contact
	CartConsumed bool
}

func encodeOrderContext(s checkout.State) string {
	raw, _ := json.Marshal(checkoutOrderContext{
		Contact:      s.Contact,
		CartConsumed: s.CartConsumed,
	})
	return string(raw)
}

func encodeOrderProgress(g checkout.Group) string {
	g.Lines, g.Quote, g.Tracking = nil, nil, nil
	raw, _ := json.Marshal(g)
	return string(raw)
}

// List выбирает страницу покупок для кабинета, панели или фоновой саги.
func (r *Checkouts) List(ctx context.Context, f checkout.Filter) ([]*checkout.Checkout, error) {
	buyerID, err := checkoutFilterUUID(f.BuyerID)
	if err != nil {
		return nil, err
	}

	afterID, err := checkoutFilterUUID(f.AfterID)
	if err != nil {
		return nil, err
	}

	rows, err := rawSQL(
		ctx,
		r.db,
		checkoutListSQL(f),
		buyerID,
		afterID,
		f.OrdersOnly,
		nullTime(f.AwaitingBefore),
		f.Limit,
		nullTime(f.WorkDueAt),
	).Rows()
	if err != nil {
		return nil, fmt.Errorf("список покупок: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var states []checkout.State

	for rows.Next() {
		var row checkoutRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, fmt.Errorf("список покупок: %w", err)
		}

		state, err := row.checkout()
		if err != nil {
			return nil, err
		}

		states = append(states, state)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := r.listGroups(ctx, states); err != nil {
		return nil, err
	}

	result := make([]*checkout.Checkout, 0, len(states))
	for _, state := range states {
		result = append(result, checkout.Restore(state))
	}

	return result, nil
}
