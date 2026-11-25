package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// lockCheckoutWork берёт страницу покупок с созревшей работой групп. Занятую покупку (её пишет
	// событие, покупатель или соседний проход) страница пропускает: её возьмёт следующий проход.
	lockCheckoutWork = `
		SELECT ` + checkoutReadColumns + `
		FROM checkouts
		WHERE id IN (SELECT checkout_id FROM checkout_groups WHERE next_work_at <= $1)
			AND id > $2
			AND (status NOT IN ('draft', 'confirmed') OR order_context->>'CartConsumed' = 'true')
		ORDER BY id
		LIMIT $3
		FOR UPDATE SKIP LOCKED`
)

// LockWorkDue — страница покупок с созревшей работой после курсора, под блокировкой до конца транзакции.
func (r *Checkouts) LockWorkDue(
	ctx context.Context,
	now time.Time,
	after ids.UUID,
	limit int,
) ([]*checkout.Checkout, error) {
	cursor := string(after)
	if cursor == "" {
		cursor = firstCheckoutID
	}
	rows, err := rawSQL(ctx, r.db, lockCheckoutWork, now, cursor, limit).Rows()
	if err != nil {
		return nil, fmt.Errorf("покупки с созревшей работой: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var states []checkout.State
	for rows.Next() {
		var row checkoutRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, fmt.Errorf("покупки с созревшей работой: %w", err)
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
	found := make([]*checkout.Checkout, 0, len(states))
	for _, state := range states {
		found = append(found, checkout.Restore(state))
	}
	return found, nil
}
