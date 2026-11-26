package checkout

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/safego"
)

// Размеры суточной сверки: потолок пачки задан договором с сервером магазина.
const (
	// ReconcileBatch — сколько заказов одного магазина уходит ему одним запросом.
	ReconcileBatch = 200
	// ReconcileRetries — сколько раз за проход повторяется запрос магазину, не ответившему в срок
	// чтения: повтор идёт отдельным кругом после остальных магазинов, без пауз, каждый раз вдвое
	// меньшей пачкой — 200, 100, 50, 25 заказов.
	ReconcileRetries = 3
	// reconcileRetryBudget — сколько от начала прохода разрешено тратить на повторные круги.
	reconcileRetryBudget = 10 * time.Minute
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

// ReconcileOrders — страховка событий: раз в сутки сверяет статусы исполняемых
// заказов одним запросом на страницу заказов магазина. Расхождение исправляется и считается.
func (s *Service) ReconcileOrders(ctx context.Context) (ReconcileResult, error) {
	now := s.clock.Now()
	run := newReconcileRun()
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
		if run.stopped() != nil || len(entries) < reconcilePage || ctx.Err() != nil {
			return s.finishReconcile(ctx, run, now)
		}
		last := entries[len(entries)-1]
		cursor = checkout.ReconcileCursor{TenantID: last.TenantID, CheckoutID: last.CheckoutID}
	}
}

// reconcilePage сверяет магазины страницы параллельно, а пачки одного магазина — по очереди:
// большой или недоступный магазин занимает одно место и не задерживает остальные. Заказы магазина,
// уже отложенного на повторный круг, к нему и добавляются; пропущенного — не сверяются.
func (s *Service) reconcilePage(ctx context.Context, entries []checkout.ReconcileEntry, run *reconcileRun) {
	var group errgroup.Group
	group.SetLimit(reconcileWorkers)
	for _, entries := range reconcileShops(entries) {
		shop := pendingShop{tenant: entries[0].TenantID, entries: entries, size: ReconcileBatch}
		if run.failed(shop.tenant) || run.joinPostponed(shop) {
			continue
		}
		group.Go(safego.Guard("checkout.reconcile", func() error {
			s.reconcileShop(ctx, shop, run)
			return nil
		}))
	}
	_ = group.Wait()
}

// reconcileShop сверяет заказы магазина пачками; на отказе остаток магазина откладывается на
// повторный круг или пропускается — магазин не спит в строю и не занимает место.
func (s *Service) reconcileShop(ctx context.Context, shop pendingShop, run *reconcileRun) {
	for len(shop.entries) > 0 && run.stopped() == nil {
		batch := shop.entries[:min(shop.size, len(shop.entries))]
		if err := s.reconcileBatch(ctx, shop.tenant, batch, run); err != nil {
			run.postpone(shop, err)
			return
		}
		run.noteAnswer()
		shop.entries = shop.entries[len(batch):]
	}
}

// reconcileBatch — один запрос магазину и применение расхождений его ответа.
func (s *Service) reconcileBatch(
	ctx context.Context,
	tenant ids.TenantID,
	entries []checkout.ReconcileEntry,
	run *reconcileRun,
) error {
	orderIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		orderIDs = append(orderIDs, entry.OrderID)
	}
	statuses, err := s.platform.OrderStatuses(ctx, tenant, orderIDs)
	if err != nil {
		return err
	}
	known := make(map[string]checkout.OrderStatus, len(statuses))
	for _, status := range statuses {
		known[status.OrderID] = status
	}
	var purchases []ports.NewTask
	for _, entry := range entries {
		status, found := known[entry.OrderID]
		outcome := ""
		switch {
		case !found:
			outcome = eventStatusMissing
		case entry.Unreachable, status.Revision > entry.Revision,
			status.Revision == entry.Revision && status.Status != entry.Status:
			outcome = s.resolveMismatch(ctx, entry, status, &purchases)
		}
		run.count(outcome)
		if outcome != "" && s.observer != nil {
			s.observer.RecordCheckoutEvent(entry.TenantID, entry.CheckoutID, outcome)
		}
	}
	s.enqueueSearchPurchases(ctx, purchases)
	return nil
}

// resolveMismatch применяет состояние из ответа пачки. Не применившееся расхождение — сигнал оператору.
func (s *Service) resolveMismatch(
	ctx context.Context,
	entry checkout.ReconcileEntry,
	status checkout.OrderStatus,
	purchases *[]ports.NewTask,
) string {
	order := checkout.SellerOrder{
		PaymentHeldUntil: status.PaymentHeldUntil,
		OrderID:          status.OrderID,
		Number:           status.Number,
		Steps:            status.Steps,
		ReturnRequest:    status.ReturnRequest,
		Status:           status.Status,
		Reason:           status.Reason,
		Tracking:         status.Tracking,
		Revision:         status.Revision,
	}
	changed, err := s.applyReconciled(ctx, entry, order, purchases)
	if err == nil && changed {
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

// applyReconciled повторяет запись один раз на конфликт версии: запись покупки идёт под блокировкой
// строки, и конфликт здесь — только защита от пути записи без блокировки.
func (s *Service) applyReconciled(
	ctx context.Context,
	entry checkout.ReconcileEntry,
	order checkout.SellerOrder,
	purchases *[]ports.NewTask,
) (bool, error) {
	var changed bool
	apply := func(ctx context.Context, tx ports.Tx) error {
		var err error
		changed, err = s.applyOrderState(ctx, tx, entry.CheckoutID, entry.TenantID, order, purchases)
		return err
	}
	err := s.uow.Do(ctx, apply)
	if errors.Is(err, checkout.ErrConflict) {
		err = s.uow.Do(ctx, apply)
	}
	return changed, err
}

// reconcileShops делит страницу, упорядоченную по магазину, на заказы отдельных магазинов.
func reconcileShops(entries []checkout.ReconcileEntry) [][]checkout.ReconcileEntry {
	var shops [][]checkout.ReconcileEntry
	start := 0
	for i := 1; i <= len(entries); i++ {
		if i == len(entries) || entries[i].TenantID != entries[start].TenantID {
			shops = append(shops, entries[start:i])
			start = i
		}
	}
	return shops
}
