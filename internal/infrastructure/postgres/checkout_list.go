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

// listGroups читает группы всей страницы, не смешивая версии покупки и её групп.
func (r *Checkouts) listGroups(ctx context.Context, states []checkout.State) error {
	if len(states) == 0 {
		return nil
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
		return fmt.Errorf("группы списка покупок: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var checkoutID string
		var row groupRow
		if err := rows.Scan(append([]any{&checkoutID}, row.targets()...)...); err != nil {
			return fmt.Errorf("группы списка покупок: %w", err)
		}

		group, err := row.checkoutGroup()
		if err != nil {
			return fmt.Errorf("позиции покупки %s: %w", checkoutID, err)
		}

		i := byID[ids.UUID(checkoutID)]
		states[i].Groups = append(states[i].Groups, group)
	}

	if err := rows.Err(); err != nil {
		return err
	}

	for _, state := range states {
		if len(state.Groups) == 0 {
			return checkout.ErrConflict
		}
	}

	return nil
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
