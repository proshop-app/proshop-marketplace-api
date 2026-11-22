package checkout

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Размеры суточной сверки: потолок пачки задан договором с сервером магазина.
const (
	// ReconcileBatch — сколько заказов одного магазина уходит ему одним запросом.
	ReconcileBatch = 200
	// reconcilePage — строк сверки за одно чтение базы.
	reconcilePage = 1000
	// reconcileWorkers — сколько магазинов сверяется одновременно.
	reconcileWorkers = 8
)

// События сверки для журнала и счётчика расхождений: каждое — пропущенное событие магазина.
const (
	eventStatusMismatch   = "order_status_mismatch"
	eventStatusMissing    = "order_status_missing"
	eventStatusUnresolved = "order_status_unresolved"
)

// ReconcileResult — итог сверки: сколько заказов проверено, сколько разошлось и чьи магазины не ответили.
type ReconcileResult struct {
	Checked    int
	Mismatched int
	Missing    int
	Unresolved int
	Failed     map[ids.TenantID]error
}

type reconcileBatch struct {
	tenant  ids.TenantID
	entries []checkout.ReconcileEntry
}

type reconcileRun struct {
	mu     sync.Mutex
	result ReconcileResult
}

// ReconcileOrders — страховка событий: раз в сутки сверяет статусы исполняемых
// заказов одним запросом на страницу заказов магазина. Расхождение исправляется и считается.
func (s *Service) ReconcileOrders(ctx context.Context) (ReconcileResult, error) {
	now := s.clock.Now()
	run := &reconcileRun{result: ReconcileResult{Failed: map[ids.TenantID]error{}}}
	expire := func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().ExpireReconciliation(ctx, now)
	}
	if err := s.uow.Do(ctx, expire); err != nil {
		return run.result, err
	}
	var cursor checkout.ReconcileCursor
	for {
		var entries []checkout.ReconcileEntry
		err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			var err error
			entries, err = tx.Checkouts().ReconcileDue(ctx, cursor, now, reconcilePage)
			return err
		})
		if err != nil {
			return run.result, err
		}
		s.reconcilePage(ctx, entries, run)
		if len(entries) < reconcilePage || ctx.Err() != nil {
			return run.result, ctx.Err()
		}
		last := entries[len(entries)-1]
		cursor = checkout.ReconcileCursor{TenantID: last.TenantID, CheckoutID: last.CheckoutID}
	}
}

// reconcilePage сверяет магазины страницы параллельно, а пачки одного магазина — по очереди:
// большой или недоступный магазин занимает одно место и после первого отказа пропускается.
func (s *Service) reconcilePage(ctx context.Context, entries []checkout.ReconcileEntry, run *reconcileRun) {
	var group errgroup.Group
	group.SetLimit(reconcileWorkers)
	for _, shop := range reconcileShops(entries) {
		group.Go(func() error {
			for _, batch := range shop {
				if run.failed(batch.tenant) || !s.reconcileBatch(ctx, batch, run) {
					return nil
				}
			}
			return nil
		})
	}
	_ = group.Wait()
}

// reconcileBatch — один запрос магазину; false — магазин не ответил.
func (s *Service) reconcileBatch(ctx context.Context, batch reconcileBatch, run *reconcileRun) bool {
	orderIDs := make([]string, 0, len(batch.entries))
	for _, entry := range batch.entries {
		orderIDs = append(orderIDs, entry.OrderID)
	}
	statuses, err := s.platform.OrderStatuses(ctx, batch.tenant, orderIDs)
	if err != nil {
		run.fail(batch.tenant, err)
		return false
	}
	known := make(map[string]checkout.OrderStatus, len(statuses))
	for _, status := range statuses {
		known[status.OrderID] = status
	}
	for _, entry := range batch.entries {
		status, found := known[entry.OrderID]
		outcome := ""
		switch {
		case !found:
			outcome = eventStatusMissing
		case entry.Unreachable, status.Revision > entry.Revision,
			status.Revision == entry.Revision && status.Status != entry.Status:
			outcome = s.resolveMismatch(ctx, entry, status)
		}
		run.count(outcome)
		if outcome != "" && s.observer != nil {
			s.observer.RecordCheckoutEvent(entry.TenantID, entry.CheckoutID, outcome)
		}
	}
	return true
}

// resolveMismatch применяет статус из ответа сверки. Заказ дочитывается целиком, только когда
// новому статусу нужны причина или отслеживание: первая сверка после простоя событий не должна
// превращаться в запрос на каждый заказ. Не применившееся расхождение — сигнал оператору.
func (s *Service) resolveMismatch(
	ctx context.Context,
	entry checkout.ReconcileEntry,
	status checkout.OrderStatus,
) string {
	order := checkout.SellerOrder{OrderID: status.OrderID, Status: status.Status, Revision: status.Revision}
	var err error
	if status.Status != entry.Status && statusNeedsDetails(status.Status) {
		order, err = s.platform.GetOrder(ctx, entry.TenantID, entry.OrderID)
	}
	changed := false
	if err == nil {
		changed, err = s.applyReconciled(ctx, entry, order)
	}
	if changed {
		return eventStatusMismatch
	}
	slog.Warn(
		"сверка заказов: расхождение не исправлено",
		slog.String("tenantId", string(entry.TenantID)),
		slog.String("checkoutId", string(entry.CheckoutID)),
		slog.String("status", string(entry.Status)),
		slog.String("shopStatus", string(status.Status)),
		slog.Any("error", err),
	)
	return eventStatusUnresolved
}

// applyReconciled повторяет запись один раз: конфликт версии — параллельное событие или действие покупателя.
func (s *Service) applyReconciled(
	ctx context.Context,
	entry checkout.ReconcileEntry,
	order checkout.SellerOrder,
) (bool, error) {
	var changed bool
	apply := func(ctx context.Context, tx ports.Tx) error {
		var err error
		changed, err = s.applyOrderState(ctx, tx, entry.CheckoutID, entry.TenantID, order)
		return err
	}
	err := s.uow.Do(ctx, apply)
	if errors.Is(err, checkout.ErrConflict) {
		err = s.uow.Do(ctx, apply)
	}
	return changed, err
}

// statusNeedsDetails — статусы, у которых покупателю показывается отслеживание или причина.
func statusNeedsDetails(status checkout.GroupStatus) bool {
	switch status {
	case checkout.GroupShipped, checkout.GroupAwaitingPickup, checkout.GroupReturned, checkout.GroupCancelled:
		return true
	}
	return false
}

func (r *reconcileRun) fail(tenant ids.TenantID, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result.Failed[tenant] = err
}

func (r *reconcileRun) failed(tenant ids.TenantID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result.Failed[tenant] != nil
}

func (r *reconcileRun) count(outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result.Checked++
	switch outcome {
	case eventStatusMismatch:
		r.result.Mismatched++
	case eventStatusMissing:
		r.result.Missing++
	case eventStatusUnresolved:
		r.result.Unresolved++
	}
}

// reconcileShops режет страницу, упорядоченную по магазину, на запросы не больше потолка договора
// и собирает запросы одного магазина вместе.
func reconcileShops(entries []checkout.ReconcileEntry) [][]reconcileBatch {
	var shops [][]reconcileBatch
	for _, entry := range entries {
		last := len(shops) - 1
		if last < 0 || shops[last][0].tenant != entry.TenantID {
			shops = append(shops, []reconcileBatch{{tenant: entry.TenantID}})
			last++
		}
		batch := len(shops[last]) - 1
		if len(shops[last][batch].entries) == ReconcileBatch {
			shops[last] = append(shops[last], reconcileBatch{tenant: entry.TenantID})
			batch++
		}
		shops[last][batch].entries = append(shops[last][batch].entries, entry)
	}
	return shops
}
