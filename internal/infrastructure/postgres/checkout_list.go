package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const selectCheckoutListGroups = `
	SELECT g.checkout_id, g.next_work_at, g.work_deferred_at IS NOT NULL, ` + groupColumns + `
	FROM unnest($1::uuid[], $2::bigint[]) AS selected (id, version)
	JOIN checkouts c ON c.id = selected.id AND c.version = selected.version
	JOIN checkout_groups g ON g.checkout_id = c.id
	ORDER BY g.checkout_id, g.tenant_id`

// loadedGroups — итог чтения групп страницы: неразобравшиеся покупки и сроки работы.
type loadedGroups struct {
	failed   map[ids.UUID]error
	nextWork map[ids.UUID]time.Time
	// deferred — у покупки есть группа с отложенной работой: её снимет сохранение.
	deferred map[ids.UUID]bool
}

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
	loaded, err := r.loadGroups(ctx, states)
	if err != nil {
		return err
	}
	for _, state := range states {
		if cause, found := loaded.failed[state.ID]; found {
			return cause
		}
	}
	return nil
}

// loadGroups дописывает группы покупкам страницы одним запросом. Покупка, чьи группы не
// разобрались или не нашлись в её версии, возвращается в failed с причиной; nextWork — ближайший
// записанный срок работы её групп.
func (r *Checkouts) loadGroups(ctx context.Context, states []checkout.State) (loadedGroups, error) {
	loaded := loadedGroups{
		failed:   map[ids.UUID]error{},
		nextWork: map[ids.UUID]time.Time{},
		deferred: map[ids.UUID]bool{},
	}
	if len(states) == 0 {
		return loaded, nil
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
		return loadedGroups{}, fmt.Errorf("группы списка покупок: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var checkoutID string
		var nextWork *time.Time
		var deferred bool
		var row groupRow
		if err := rows.Scan(append([]any{&checkoutID, &nextWork, &deferred}, row.targets()...)...); err != nil {
			return loadedGroups{}, fmt.Errorf("группы списка покупок: %w", err)
		}
		loaded.noteWork(ids.UUID(checkoutID), nextWork)
		if deferred {
			loaded.deferred[ids.UUID(checkoutID)] = true
		}
		group, err := row.checkoutGroup()
		if err != nil {
			loaded.failed[ids.UUID(checkoutID)] = fmt.Errorf("позиции покупки %s: %w", checkoutID, err)
			continue
		}
		i := byID[ids.UUID(checkoutID)]
		states[i].Groups = append(states[i].Groups, group)
	}
	if err := rows.Err(); err != nil {
		return loadedGroups{}, err
	}
	for _, state := range states {
		if _, found := loaded.failed[state.ID]; !found && len(state.Groups) == 0 {
			loaded.failed[state.ID] = checkout.ErrConflict
		}
	}
	return loaded, nil
}

// noteWork запоминает ближайший срок работы групп покупки.
func (l loadedGroups) noteWork(id ids.UUID, next *time.Time) {
	if next == nil {
		return
	}
	if current, found := l.nextWork[id]; !found || next.Before(current) {
		l.nextWork[id] = *next
	}
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
