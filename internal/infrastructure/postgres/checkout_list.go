package postgres

import (
	"context"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const selectCheckoutListGroups = `
	SELECT g.checkout_id, ` + groupColumns + `
	FROM unnest($1::uuid[], $2::bigint[]) AS selected (id, version)
	JOIN checkouts c ON c.id = selected.id AND c.version = selected.version
	JOIN checkout_groups g ON g.checkout_id = c.id
	ORDER BY g.checkout_id, g.tenant_id`

func checkoutListSQL(filter checkout.Filter) string {
	switch {
	case !filter.StuckBefore.IsZero():
		return listStuckCheckouts
	case filter.BuyerID != "" && filter.AfterID != "":
		return listBuyerCheckoutsAfter
	case filter.BuyerID != "":
		return listBuyerCheckouts
	case filter.AfterID != "":
		return listCheckoutsAfter
	default:
		return listAllCheckouts
	}
}

// listGroups читает группы всей страницы, не смешивая версии покупки и её групп; первая
// неразобравшаяся покупка — ошибка страницы.
func (r *Checkouts) listGroups(ctx context.Context, states []checkout.State) error {
	failed, err := r.loadGroups(ctx, states)
	if err != nil {
		return err
	}
	for _, state := range states {
		if cause, found := failed[state.ID]; found {
			return cause
		}
	}
	return nil
}

// loadGroups дописывает группы покупкам страницы одним запросом. Покупка, чьи группы не
// разобрались или не нашлись в её версии, возвращается в failed с причиной.
func (r *Checkouts) loadGroups(ctx context.Context, states []checkout.State) (map[ids.UUID]error, error) {
	failed := map[ids.UUID]error{}
	if len(states) == 0 {
		return failed, nil
	}
	checkoutIDs := make([]string, len(states))
	versions := make([]int64, len(states))
	byID := make(map[ids.UUID]int, len(states))
	for i, state := range states {
		checkoutIDs[i], versions[i] = string(state.ID), state.Version
		byID[state.ID] = i
	}
	rows, err := rawSQL(ctx, r.db, selectCheckoutListGroups, checkoutIDs, versions).Rows()
	if err != nil {
		return nil, fmt.Errorf("группы списка покупок: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var checkoutID string
		var row groupRow
		if err := rows.Scan(append([]any{&checkoutID}, row.targets()...)...); err != nil {
			return nil, fmt.Errorf("группы списка покупок: %w", err)
		}
		group, err := row.checkoutGroup()
		if err != nil {
			failed[ids.UUID(checkoutID)] = fmt.Errorf("позиции покупки %s: %w", checkoutID, err)
			continue
		}
		i := byID[ids.UUID(checkoutID)]
		states[i].Groups = append(states[i].Groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, state := range states {
		if _, found := failed[state.ID]; !found && len(state.Groups) == 0 {
			failed[state.ID] = checkout.ErrConflict
		}
	}
	return failed, nil
}

func checkoutFilterUUID(value ids.UUID) (any, error) {
	if value == "" {
		return nil, nil
	}

	if _, err := ids.ParseUUID(string(value)); err != nil {
		return nil, err
	}

	return string(value), nil
}
