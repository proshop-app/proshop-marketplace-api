package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// selectReconcileDue идёт по частичному индексу (tenant_id, checkout_id): заказы одного магазина
	// выходят подряд и уходят ему одним запросом.
	selectReconcileDue = `
		SELECT checkout_id, tenant_id,
			COALESCE(order_progress->>'OrderID', ''),
			COALESCE(order_progress->>'Status', ''),
			COALESCE(order_progress->>'LastOrderStatus', ''),
			order_progress->'OrderRevision'
		FROM checkout_groups
		WHERE reconcile_until > $1 AND (tenant_id, checkout_id) > ($2, $3::uuid)
		ORDER BY tenant_id, checkout_id
		LIMIT $4`

	// expireReconciliation снимает истёкшие сроки, пропуская строки групп, занятые записью покупки:
	// многострочное обновление без порядка иначе сцепилось бы с записью страницы восстановления.
	// Пропущенную группу снимет её же сохранение (срок пересчитывается) или следующая сверка.
	expireReconciliation = `
		UPDATE checkout_groups
		SET reconcile_until = NULL
		WHERE (checkout_id, tenant_id) IN (
			SELECT checkout_id, tenant_id FROM checkout_groups
			WHERE reconcile_until <= $1
			FOR NO KEY UPDATE SKIP LOCKED
		)`

	// firstCheckoutID — начало обхода: меньше любого идентификатора покупки; общий для обходов покупок.
	firstCheckoutID = "00000000-0000-0000-0000-000000000000"
)

// ReconcileDue читает страницу заказов суточной сверки без снимков покупок.
func (r *Checkouts) ReconcileDue(
	ctx context.Context,
	after checkout.ReconcileCursor,
	now time.Time,
	limit int,
) ([]checkout.ReconcileEntry, error) {
	checkoutID := string(after.CheckoutID)
	if checkoutID == "" {
		checkoutID = firstCheckoutID
	}
	rows, err := rawSQL(ctx, r.db, selectReconcileDue, now, string(after.TenantID), checkoutID, limit).Rows()
	if err != nil {
		return nil, fmt.Errorf("сверка заказов: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var entries []checkout.ReconcileEntry
	for rows.Next() {
		var (
			entry              checkout.ReconcileEntry
			checkoutID, tenant string
			last               checkout.GroupStatus
			revision           []byte
		)
		if err := rows.Scan(&checkoutID, &tenant, &entry.OrderID, &entry.Status, &last, &revision); err != nil {
			return nil, fmt.Errorf("сверка заказов: %w", err)
		}
		entry.CheckoutID, entry.TenantID = ids.UUID(checkoutID), ids.TenantID(tenant)
		entry.Revision, entry.Failure = reconcileRevision(revision)
		if entry.OrderID == "" && entry.Failure == nil {
			entry.Failure = fmt.Errorf("заказ для сверки отсутствует")
		}
		// Недоступный при прежнем опросе магазин: сверяется последний подтверждённый статус.
		if entry.Status == checkout.GroupSellerUnreachable {
			entry.Status, entry.Unreachable = last, true
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func reconcileRevision(raw []byte) (int64, error) {
	var revision *int64
	if err := json.Unmarshal(raw, &revision); err != nil {
		return 0, fmt.Errorf("неверный OrderRevision: %w", err)
	}
	if revision == nil || *revision < 0 {
		return 0, fmt.Errorf("неверный OrderRevision")
	}
	return *revision, nil
}

// ExpireReconciliation снимает со сверки заказы, которые не двигаются дольше срока сверки.
func (r *Checkouts) ExpireReconciliation(ctx context.Context, now time.Time) error {
	if _, err := execSQL(ctx, r.db, expireReconciliation, now); err != nil {
		return fmt.Errorf("срок сверки заказов: %w", err)
	}
	return nil
}
