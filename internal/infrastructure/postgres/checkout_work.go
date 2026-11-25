package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// selectWorkCandidates идёт по индексу checkout_groups_next_work в его порядке: обход ограничен
	// созревшими группами страницы при любой статистике, история покупок не читается.
	selectWorkCandidates = `
		SELECT checkout_id, next_work_at
		FROM checkout_groups
		WHERE next_work_at <= $1 AND (next_work_at, checkout_id) > ($2, $3::uuid)
		ORDER BY next_work_at, checkout_id
		LIMIT $4`

	// lockWorkCheckouts берёт покупки страницы только по ключу. Занятую покупку (её пишет событие,
	// покупатель или соседний проход) страница пропускает: её возьмёт следующий проход. Созревшую
	// работу перепроверяют группы, прочитанные после блокировки (loadGroups), а не условие здесь:
	// условие по группам разворачивало бы запрос от всех созревших групп.
	lockWorkCheckouts = `
		SELECT ` + checkoutReadColumns + `
		FROM checkouts
		WHERE id = ANY($1::uuid[])
			AND (status NOT IN ('draft', 'confirmed') OR order_context->>'CartConsumed' = 'true')
		ORDER BY id
		FOR NO KEY UPDATE SKIP LOCKED`

	deferCheckoutWork = `
		UPDATE checkout_groups
		SET next_work_at = $2,
			work_deferred_at = $3
		WHERE checkout_id = ANY($1::uuid[]) AND next_work_at < $2`
)

// LockWorkDue — покупки страницы созревших групп после курсора, под блокировкой до конца транзакции.
func (r *Checkouts) LockWorkDue(
	ctx context.Context,
	now time.Time,
	after checkout.WorkCursor,
	limit int,
) (checkout.WorkPage, error) {
	candidates, page, err := r.workCandidates(ctx, now, after, limit)
	if err != nil || len(candidates) == 0 {
		return page, err
	}
	states, broken, err := r.lockWorkCheckouts(ctx, candidates)
	if err != nil {
		return checkout.WorkPage{}, err
	}
	loaded, err := r.loadGroups(ctx, states)
	if err != nil {
		return checkout.WorkPage{}, err
	}
	page.Broken = broken
	for _, state := range states {
		if cause, found := loaded.failed[state.ID]; found {
			page.Broken = append(page.Broken, checkout.BrokenCheckout{ID: state.ID, Err: cause})
			continue
		}
		// Соседний проход сохранил покупку между выборкой кандидатов и блокировкой: работы нет.
		if next, found := loaded.nextWork[state.ID]; found && !next.After(now) {
			page.Due = append(page.Due, checkout.Restore(state))
		}
	}
	return page, nil
}

// DeferWork переносит созревшую работу групп покупок на until и отмечает её отложенной в now.
func (r *Checkouts) DeferWork(ctx context.Context, checkoutIDs []ids.UUID, until, now time.Time) error {
	if len(checkoutIDs) == 0 {
		return nil
	}
	list := make([]string, 0, len(checkoutIDs))
	for _, id := range checkoutIDs {
		list = append(list, string(id))
	}
	if _, err := execSQL(ctx, r.db, deferCheckoutWork, list, until, now); err != nil {
		return fmt.Errorf("отложить работу покупок: %w", err)
	}
	return nil
}

// workCandidates — покупки страницы созревших групп без повторов и курсор продолжения.
func (r *Checkouts) workCandidates(
	ctx context.Context,
	now time.Time,
	after checkout.WorkCursor,
	limit int,
) ([]string, checkout.WorkPage, error) {
	cursorID := string(after.CheckoutID)
	if cursorID == "" {
		cursorID = firstCheckoutID
	}
	rows, err := rawSQL(ctx, r.db, selectWorkCandidates, now, after.At, cursorID, limit).Rows()
	if err != nil {
		return nil, checkout.WorkPage{}, fmt.Errorf("созревшая работа покупок: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var page checkout.WorkPage
	var candidates []string
	seen, groups := map[string]bool{}, 0
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, checkout.WorkPage{}, fmt.Errorf("созревшая работа покупок: %w", err)
		}
		groups++
		page.Next = checkout.WorkCursor{At: at, CheckoutID: ids.UUID(id)}
		if !seen[id] {
			seen[id] = true
			candidates = append(candidates, id)
		}
	}
	page.More = groups == limit
	return candidates, page, rows.Err()
}

// lockWorkCheckouts блокирует покупки страницы; строка, которая не разбирается, идёт в broken.
func (r *Checkouts) lockWorkCheckouts(
	ctx context.Context,
	candidates []string,
) ([]checkout.State, []checkout.BrokenCheckout, error) {
	rows, err := rawSQL(ctx, r.db, lockWorkCheckouts, candidates).Rows()
	if err != nil {
		return nil, nil, fmt.Errorf("покупки с созревшей работой: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var states []checkout.State
	var broken []checkout.BrokenCheckout
	for rows.Next() {
		var row checkoutRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, nil, fmt.Errorf("покупки с созревшей работой: %w", err)
		}
		state, err := row.checkout()
		if err != nil {
			broken = append(broken, checkout.BrokenCheckout{ID: ids.UUID(row.id), Err: err})
			continue
		}
		states = append(states, state)
	}
	return states, broken, rows.Err()
}
