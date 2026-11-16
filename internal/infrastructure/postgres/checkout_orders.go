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
 AND (NOT $7 OR EXISTS (SELECT 1 FROM checkout_groups g
  WHERE g.checkout_id = c.id AND coalesce(g.order_progress->>'OrderID', '') <> ''))
  AND ($4::timestamptz IS NULL OR EXISTS (
   SELECT 1 FROM checkout_groups g
   WHERE g.checkout_id = c.id AND (g.order_progress->>'ReconciliationRequired' = 'true'
    OR (g.order_progress->>'Status' = 'awaiting_payment'
     AND (g.order_progress->>'AwaitingSince')::timestamptz <= $4))
  ))
 AND ($6::timestamptz IS NULL OR c.id IN (
 SELECT checkout_id FROM checkout_groups WHERE next_work_at <= $6
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
	PaymentAttempt string
	PaymentStarted bool
	Contact        checkout.Contact
	CartConsumed   bool
}

func encodeOrderContext(s checkout.State) string {
	raw, _ := json.Marshal(checkoutOrderContext{
		Contact:        s.Contact,
		PaymentAttempt: s.PaymentAttempt,
		PaymentStarted: s.PaymentStarted,
		CartConsumed:   s.CartConsumed,
	})
	return string(raw)
}

func encodeOrderProgress(g checkout.Group) string {
	g.Lines, g.Quote, g.Tracking = nil, nil, nil
	raw, _ := json.Marshal(g)
	return string(raw)
}

// List выбирает страницу покупок для кабинета, панели или фоновой сверки.
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
		nullTime(f.StuckBefore),
		f.Limit,
		nullTime(f.WorkDueAt),
		f.HasOrders,
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
