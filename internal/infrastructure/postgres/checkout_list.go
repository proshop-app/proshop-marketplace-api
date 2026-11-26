package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const selectCheckoutListGroups = `
	SELECT c.id, g.checkout_id IS NOT NULL, ` + groupColumns + `, g.next_work_at, g.work_deferred_at IS NOT NULL
	FROM unnest($1::uuid[], $2::bigint[]) AS selected (id, version)
	JOIN checkouts c ON c.id = selected.id AND c.version = selected.version
	LEFT JOIN LATERAL (
		SELECT * FROM checkout_groups WHERE checkout_id = c.id OFFSET 0
	) g ON true
	ORDER BY c.id, g.tenant_id`

var errCheckoutGroupsMissing = errors.New("группы отсутствуют у сохранённой версии покупки")

// loadedGroups — итог чтения групп страницы: неразобравшиеся покупки и сроки работы.
type loadedGroups struct {
	failed   map[ids.UUID][]checkout.ReadFailure
	nextWork map[ids.UUID]time.Time
	// deferred — у покупки есть группа с отложенной работой: её снимет сохранение.
	deferred map[ids.UUID]bool
}

func checkoutListSQL(filter checkout.Filter, buyerID, afterID any) (string, []any) {
	if !filter.StuckBefore.IsZero() {
		args := []any{filter.StuckBefore, filter.Limit}
		if filter.BuyerID != "" || filter.HasOrders {
			return listFilteredStuckCheckouts, append(args, filter.AfterStuckAt, afterID, buyerID, filter.HasOrders)
		}
		if afterID != nil {
			return listStuckCheckoutsAfter, append(args, filter.AfterStuckAt, afterID)
		}
		return listStuckCheckoutsFirst, args
	}

	args := []any{buyerID, afterID, filter.OrdersOnly, filter.Limit, filter.HasOrders}
	switch {
	case filter.BuyerID != "" && filter.AfterID != "":
		return listBuyerCheckoutsAfter, args
	case filter.BuyerID != "":
		return listBuyerCheckouts, args
	case filter.AfterID != "":
		return listCheckoutsAfter, args
	default:
		return listAllCheckouts, args
	}
}

// listEntry сохраняет ключ страницы и принадлежность даже при ошибке разбора снимка.
func (r *checkoutRow) listEntry() (checkout.ListEntry, checkout.State) {
	entry := checkout.ListEntry{Cursor: checkout.ListCursor{ID: ids.UUID(r.id), StuckAt: r.stuckAt}}
	state, err := r.checkout()
	if err != nil {
		entry.Failures = append(entry.Failures, checkout.ReadFailure{
			ID:      state.ID,
			BuyerID: state.BuyerID,
			Cause:   err,
			Reason:  checkout.ReadHeader,
		})
	}
	if r.invalidStuckAt || (r.stuckAt != nil && !checkout.ValidStuckCursor(r.stuckAt)) {
		entry.Failures = append(entry.Failures, checkout.ReadFailure{
			ID:      state.ID,
			BuyerID: state.BuyerID,
			Cause:   errors.New("некорректное время попадания в зависшие покупки"),
			Reason:  checkout.ReadCursor,
		})
	}
	return entry, state
}

// failures связывает ошибку строки покупки с магазинами её групп.
func (l loadedGroups) failures(entry checkout.ListEntry, state checkout.State) []checkout.ReadFailure {
	failures := l.failed[state.ID]
	if len(entry.Failures) == 0 {
		return failures
	}
	if len(state.Groups) == 0 && len(failures) == 0 {
		return entry.Failures
	}

	tenants := make([]ids.TenantID, 0, len(state.Groups)+len(failures))
	for _, group := range state.Groups {
		tenants = append(tenants, group.TenantID)
	}
	for _, failure := range failures {
		tenants = append(tenants, failure.TenantID)
	}
	for _, header := range entry.Failures {
		for _, tenant := range tenants {
			failure := header
			failure.TenantID = tenant
			failures = append(failures, failure)
		}
	}
	return failures
}

// loadGroups дописывает группы покупкам страницы одним запросом. Покупка, чьи группы не
// разобрались или не нашлись в её версии, возвращается в failed с причиной; nextWork — ближайший
// записанный срок работы её групп.
func (r *Checkouts) loadGroups(ctx context.Context, states []checkout.State) (loadedGroups, error) {
	loaded := loadedGroups{
		failed:   map[ids.UUID][]checkout.ReadFailure{},
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
		var nextWork pgtype.Timestamptz
		var present, deferred bool
		var row groupRow
		targets := append([]any{&checkoutID, &present}, row.targets()...)
		if err := rows.Scan(append(targets, &nextWork, &deferred)...); err != nil {
			return loadedGroups{}, fmt.Errorf("строка группы покупки: %w", err)
		}
		id := ids.UUID(checkoutID)
		i, found := byID[id]
		if !found {
			return loadedGroups{}, fmt.Errorf("не прочитан ключ покупки группы %q", checkoutID)
		}
		if deferred {
			loaded.deferred[id] = true
		}
		var tenant ids.TenantID
		var group checkout.Group
		var groupErr error
		if present {
			tenant = ids.TenantID(*row.tenantID)
			group, groupErr = row.checkoutGroup()
			if nextWork.InfinityModifier != pgtype.Finite {
				groupErr = errors.Join(groupErr, errors.New("бесконечный срок работы группы"))
			}
		} else {
			groupErr = errCheckoutGroupsMissing
		}
		if groupErr != nil {
			loaded.failed[id] = append(loaded.failed[id], checkout.ReadFailure{
				ID:       id,
				BuyerID:  states[i].BuyerID,
				TenantID: tenant,
				Cause:    groupErr,
				Reason:   checkout.ReadGroup,
			})
			continue
		}
		if nextWork.Valid {
			loaded.noteWork(id, nextWork.Time)
		}
		states[i].Groups = append(states[i].Groups, group)
	}
	if err := rows.Err(); err != nil {
		return loadedGroups{}, err
	}
	for _, state := range states {
		if _, found := loaded.failed[state.ID]; !found && len(state.Groups) == 0 {
			loaded.failed[state.ID] = []checkout.ReadFailure{{
				ID:      state.ID,
				BuyerID: state.BuyerID,
				Cause:   checkout.ErrConflict,
			}}
		}
	}
	return loaded, nil
}

// noteWork запоминает ближайший срок работы групп покупки.
func (l loadedGroups) noteWork(id ids.UUID, next time.Time) {
	if current, found := l.nextWork[id]; !found || next.Before(current) {
		l.nextWork[id] = next
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
