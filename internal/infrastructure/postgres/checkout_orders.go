package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	listCheckouts = `
 SELECT ` + checkoutReadColumns + `
 FROM checkouts c
 WHERE `

	listCheckoutConditions = `
  AND (NOT $3 OR status NOT IN ('draft', 'confirmed') OR order_context->>'CartConsumed' = 'true')
 AND (NOT $5 OR EXISTS (SELECT 1 FROM checkout_groups g
  WHERE g.checkout_id = c.id AND coalesce(g.order_progress->>'OrderID', '') <> ''))`

	listCheckoutOrder = `
 ORDER BY id
 LIMIT $4`

	// Выражение совпадает с индексом 00074. Даже повреждённая дата получает конечный ключ обхода.
	stuckCursorKey = `(CASE WHEN stuck_at >= '0001-01-01 00:00:00+00'::timestamptz
		AND stuck_at < '10000-01-01 00:00:00+00'::timestamptz
		THEN stuck_at ELSE '0001-01-01 00:00:00+00'::timestamptz END)`

	listStuckCheckouts = `
		SELECT page.cursor_at, page.invalid_stuck_at, c.*
		FROM (
			SELECT ` + stuckCursorKey + ` AS cursor_at, stuck_at <> ` + stuckCursorKey + ` AS invalid_stuck_at,
				checkout_id
			FROM checkout_groups g
			WHERE stuck_at IS NOT NULL AND ` + stuckCursorKey + ` <= $1`

	// Внутренний предел известен общему подготовленному плану даже при неизвестных параметрах.
	// Он ограничивает стоимость отбора, а заголовки читаются по ключам уже отобранной страницы.
	listStuckCheckoutsOrder = `
			ORDER BY cursor_at, checkout_id LIMIT 100
		) page
		JOIN LATERAL (
			SELECT ` + checkoutReadColumns + ` FROM checkouts c WHERE id = page.checkout_id OFFSET 0
		) c ON true
		ORDER BY page.cursor_at, page.checkout_id LIMIT $2`
	listStuckCheckoutsFirst = listStuckCheckouts + listStuckCheckoutsOrder
	listStuckCheckoutsAfter = listStuckCheckouts + `
		AND (` + stuckCursorKey + `, checkout_id) > ($3::timestamptz, $4::uuid)` + listStuckCheckoutsOrder

	// Фильтры покупателя и заказа применяются до LIMIT. Общая страница панели не читает эти таблицы при отборе.
	listFilteredStuckCheckouts = listStuckCheckouts + `
		AND ($3::timestamptz IS NULL OR (` + stuckCursorKey + `, checkout_id) > ($3, $4::uuid))
		AND ($5::uuid IS NULL OR EXISTS (
			SELECT 1 FROM checkouts owner WHERE owner.id = g.checkout_id AND owner.buyer_id = $5
		))
		AND (NOT $6 OR EXISTS (
			SELECT 1 FROM checkout_groups ordered
			WHERE ordered.checkout_id = g.checkout_id AND coalesce(ordered.order_progress->>'OrderID', '') <> ''
		))` + listStuckCheckoutsOrder

	// Отдельные формы сохраняют индексные условия и при generic prepared plan.
	listCheckoutFilters     = listCheckoutConditions + listCheckoutOrder
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

// List читает покупки для кабинета и панели, сохраняя ошибки отдельных строк.
func (r *Checkouts) List(ctx context.Context, f checkout.Filter) ([]checkout.ListEntry, error) {
	buyerID, err := checkoutFilterUUID(f.BuyerID)
	if err != nil {
		return nil, err
	}

	afterID, err := checkoutFilterUUID(f.AfterID)
	if err != nil {
		return nil, err
	}

	stuck := !f.StuckBefore.IsZero()
	if stuck && !f.ValidStuckFilter() {
		return nil, ids.ErrInvalid
	}
	query, args := checkoutListSQL(f, buyerID, afterID)
	rows, err := rawSQL(ctx, r.db, query, args...).Rows()
	if err != nil {
		return nil, fmt.Errorf("список покупок: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var states []checkout.State
	var result []checkout.ListEntry

	for rows.Next() {
		var row checkoutRow
		targets := row.targets()
		if stuck {
			targets = append([]any{&row.stuckAt, &row.invalidStuckAt}, targets...)
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, fmt.Errorf("строка списка покупок: %w", err)
		}
		entry, state := row.listEntry()
		result = append(result, entry)
		states = append(states, state)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	loaded, err := r.loadGroups(ctx, states)
	if err != nil {
		return nil, err
	}
	for i, state := range states {
		result[i].Failures = loaded.failures(result[i], state)
		if len(result[i].Failures) == 0 {
			result[i].Checkout = checkout.Restore(state)
		} else if errors.Is(result[i].ReadError(), checkout.ErrConflict) {
			return nil, checkout.ErrConflict
		}
	}
	return result, nil
}
