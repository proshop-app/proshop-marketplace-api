package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

const (
	// updateCheckouts пишет пачку покупок одним запросом, каждую — условно по исходным статусу и версии.
	updateCheckouts = `
		UPDATE checkouts c
		SET status = v.status,
			carrier = v.carrier,
			pickup_point_id = v.pickup_point_id,
			delivery = v.delivery::jsonb,
			goods_total = v.goods_total,
			delivery_total = v.delivery_total,
			total = v.total,
			version = v.version,
			updated_at = v.updated_at,
			confirmed_at = v.confirmed_at,
			order_context = v.order_context::jsonb
		FROM unnest(
			$1::uuid[], $2::text[], $3::bigint[], $4::text[], $5::text[], $6::text[], $7::text[],
			$8::bigint[], $9::bigint[], $10::bigint[], $11::bigint[], $12::timestamptz[], $13::timestamptz[],
			$14::text[]
		) AS v (
			id, origin_status, origin_version, status, carrier, pickup_point_id, delivery,
			goods_total, delivery_total, total, version, updated_at, confirmed_at, order_context
		)
		WHERE c.id = v.id AND c.status = v.origin_status AND c.version = v.origin_version`

	// checkoutGroupValues — строки групп пачки в порядке groupColumns, перед ними — покупка.
	checkoutGroupValues = `
		unnest(
			$1::uuid[], $2::text[], $3::text[], $4::text[], $5::bigint[], $6::text[], $7::text[],
			$8::bigint[], $9::int[], $10::int[], $11::timestamptz[], $12::text[], $13::text[],
			$14::timestamptz[], $15::timestamptz[]
		) AS v (
			checkout_id, tenant_id, external_checkout_id, lines, goods, quote_status, quote_reason,
			delivery_cost, min_days, max_days, quote_expires_at, order_progress, tracking,
			next_work_at, reconcile_until
		)`

	insertCheckoutGroups = `
		INSERT INTO checkout_groups (checkout_id, ` + groupColumns + `, next_work_at, reconcile_until)
		SELECT checkout_id, tenant_id, external_checkout_id, lines::jsonb, goods, quote_status, quote_reason,
			delivery_cost, min_days, max_days, quote_expires_at, order_progress::jsonb, tracking::jsonb,
			next_work_at, reconcile_until
		FROM ` + checkoutGroupValues

	// updateCheckoutGroups переписывает только изменившиеся группы: событие одного магазина
	// не порождает новых версий строк остальных групп покупки.
	updateCheckoutGroups = `
		UPDATE checkout_groups g
		SET external_checkout_id = v.external_checkout_id,
			lines = v.lines::jsonb,
			goods = v.goods,
			quote_status = v.quote_status,
			quote_reason = v.quote_reason,
			delivery_cost = v.delivery_cost,
			min_days = v.min_days,
			max_days = v.max_days,
			quote_expires_at = v.quote_expires_at,
			order_progress = v.order_progress::jsonb,
			tracking = v.tracking::jsonb,
			next_work_at = v.next_work_at,
			reconcile_until = v.reconcile_until,
			work_deferred_at = NULL
		FROM ` + checkoutGroupValues + `
		WHERE g.checkout_id = v.checkout_id AND g.tenant_id = v.tenant_id
			AND ((
				g.external_checkout_id, g.lines, g.goods, g.quote_status, g.quote_reason, g.delivery_cost,
				g.min_days, g.max_days, g.quote_expires_at, g.order_progress, g.tracking, g.next_work_at,
				g.reconcile_until
			) IS DISTINCT FROM (
				v.external_checkout_id, v.lines::jsonb, v.goods, v.quote_status, v.quote_reason, v.delivery_cost,
				v.min_days, v.max_days, v.quote_expires_at, v.order_progress::jsonb, v.tracking::jsonb,
				v.next_work_at, v.reconcile_until
			) OR g.work_deferred_at IS NOT NULL)`
)

// checkoutColumnsBatch — колонки пачки покупок для unnest, в порядке updateCheckouts.
type checkoutColumnsBatch struct {
	ids, originStatuses, statuses []string
	originVersions, versions      []int64
	carriers, points, selections  []*string
	goods, deliveries, totals     []int64
	updatedAt                     []time.Time
	confirmedAt                   []*time.Time
	orderContexts                 []string
}

// groupColumnsBatch — колонки групп пачки для unnest, в порядке checkoutGroupValues.
type groupColumnsBatch struct {
	checkoutIDs, tenants, externals, lines []string
	goods                                  []int64
	quoteStatuses, quoteReasons            []*string
	costs                                  []*int64
	minDays, maxDays                       []*int32
	quoteExpiresAt                         []*time.Time
	progress                               []string
	tracking                               []*string
	nextWorkAt, reconcileUntil             []*time.Time
}

// UpdateMany сохраняет пачку покупок двумя запросами: покупки и их изменившиеся группы. Хоть одна
// покупка изменена параллельно — ErrConflict, и транзакция откатывает всю пачку.
func (r *Checkouts) UpdateMany(ctx context.Context, cs []*checkout.Checkout) error {
	if len(cs) == 0 {
		return nil
	}
	states := make([]checkout.State, 0, len(cs))
	var batch checkoutColumnsBatch
	for _, c := range cs {
		status, version := c.Origin()
		if err := batch.add(c.State(), status, version); err != nil {
			return err
		}
		states = append(states, c.State())
	}
	affected, err := execSQL(ctx, r.db, updateCheckouts, batch.args()...)
	if err != nil {
		return fmt.Errorf("покупки: %w", err)
	}
	if affected != int64(len(cs)) {
		return checkout.ErrConflict
	}
	if err := r.writeGroups(ctx, updateCheckoutGroups, states); err != nil {
		return err
	}
	for _, c := range cs {
		c.MarkSaved()
	}
	return nil
}

// writeGroups пишет группы пачки покупок одним запросом вставки или обновления.
func (r *Checkouts) writeGroups(ctx context.Context, sql string, states []checkout.State) error {
	var batch groupColumnsBatch
	for _, s := range states {
		for _, group := range s.Groups {
			if err := batch.add(string(s.ID), group); err != nil {
				return fmt.Errorf("группа покупки %s: %w", s.ID, err)
			}
		}
	}
	if _, err := execSQL(ctx, r.db, sql, batch.args()...); err != nil {
		return fmt.Errorf("группы покупок: %w", err)
	}
	return nil
}

func (b *checkoutColumnsBatch) add(s checkout.State, originStatus checkout.Status, originVersion int64) error {
	carrier, point, selection, err := selectionColumns(s.Delivery)
	if err != nil {
		return err
	}
	b.ids = append(b.ids, string(s.ID))
	b.originStatuses = append(b.originStatuses, string(originStatus))
	b.originVersions = append(b.originVersions, originVersion)
	b.statuses = append(b.statuses, string(s.Status))
	b.carriers = append(b.carriers, carrier)
	b.points = append(b.points, point)
	b.selections = append(b.selections, selection)
	b.goods = append(b.goods, s.GoodsTotal.Kopecks())
	b.deliveries = append(b.deliveries, s.DeliveryTotal.Kopecks())
	b.totals = append(b.totals, s.Total.Kopecks())
	b.versions = append(b.versions, s.Version)
	b.updatedAt = append(b.updatedAt, s.UpdatedAt)
	b.confirmedAt = append(b.confirmedAt, timeColumn(s.ConfirmedAt))
	b.orderContexts = append(b.orderContexts, encodeOrderContext(s))
	return nil
}

func (b *checkoutColumnsBatch) args() []any {
	return []any{
		b.ids,
		b.originStatuses,
		b.originVersions,
		b.statuses,
		b.carriers,
		b.points,
		b.selections,
		b.goods,
		b.deliveries,
		b.totals,
		b.versions,
		b.updatedAt,
		b.confirmedAt,
		b.orderContexts,
	}
}

func (b *groupColumnsBatch) add(checkoutID string, group checkout.Group) error {
	lines, err := encodeLines(group)
	if err != nil {
		return err
	}
	tracking, err := jsonValue(group.Tracking, "")
	if err != nil {
		return err
	}
	b.checkoutIDs = append(b.checkoutIDs, checkoutID)
	b.tenants = append(b.tenants, string(group.TenantID))
	b.externals = append(b.externals, group.ExternalCheckoutID)
	b.lines = append(b.lines, lines)
	b.goods = append(b.goods, group.Goods.Kopecks())
	b.addQuote(group.Quote)
	b.progress = append(b.progress, encodeOrderProgress(group))
	b.tracking = append(b.tracking, textColumn(tracking))
	b.nextWorkAt = append(b.nextWorkAt, nextCheckoutWork(group))
	b.reconcileUntil = append(b.reconcileUntil, timeColumn(group.ReconcileUntil()))
	return nil
}

// addQuote — поля расчёта доставки; суммы и сроки есть только у доступного расчёта.
func (b *groupColumnsBatch) addQuote(q *delivery.Quote) {
	var status, reason *string
	var cost *int64
	var minDays, maxDays *int32
	var expiresAt *time.Time
	if q != nil {
		status, reason, expiresAt = textColumn(string(q.Status)), textColumn(q.Reason), &q.ExpiresAt
	}
	if q != nil && q.Status == delivery.QuoteAvailable {
		kopecks, low, high := q.Cost.Kopecks(), int32(q.MinDays), int32(q.MaxDays) //nolint:gosec // дни доставки
		cost, minDays, maxDays = &kopecks, &low, &high
	}
	b.quoteStatuses = append(b.quoteStatuses, status)
	b.quoteReasons = append(b.quoteReasons, reason)
	b.costs = append(b.costs, cost)
	b.minDays = append(b.minDays, minDays)
	b.maxDays = append(b.maxDays, maxDays)
	b.quoteExpiresAt = append(b.quoteExpiresAt, expiresAt)
}

func (b *groupColumnsBatch) args() []any {
	return []any{
		b.checkoutIDs,
		b.tenants,
		b.externals,
		b.lines,
		b.goods,
		b.quoteStatuses,
		b.quoteReasons,
		b.costs,
		b.minDays,
		b.maxDays,
		b.quoteExpiresAt,
		b.progress,
		b.tracking,
		b.nextWorkAt,
		b.reconcileUntil,
	}
}

func encodeLines(group checkout.Group) (string, error) {
	docs := make([]lineJSON, 0, len(group.Lines))
	for _, line := range group.Lines {
		docs = append(docs, lineJSON{
			ProductID: string(line.Offer.Product),
			Name:      line.Name,
			Quantity:  line.Quantity,
			Price:     line.Price.Kopecks(),
			Discounts: line.Discounts,
		})
	}
	raw, err := json.Marshal(docs)
	if err != nil {
		return "", fmt.Errorf("позиции покупки: %w", err)
	}
	return string(raw), nil
}

func selectionColumns(selection *checkout.Selection) (carrier, point, encoded *string, err error) {
	if selection == nil {
		return nil, nil, nil, nil
	}
	raw, err := json.Marshal(selectionJSON{
		City:       selection.City,
		Address:    selection.Address,
		RegionCode: selection.RegionCode,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("доставка покупки: %w", err)
	}
	name, text := string(selection.Carrier), string(raw)
	return &name, &selection.PickupPointCode, &text, nil
}

// Проекция очередного опроса/возврата позволяет не читать JSON всей истории в Resume.
func nextCheckoutWork(g checkout.Group) *time.Time {
	next, found := g.NextWorkAt()
	if !found {
		return nil
	}
	return &next
}

// textColumn — строка для колонки массива; пустая — NULL.
func textColumn(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// timeColumn — время для колонки массива; нулевое — NULL.
func timeColumn(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}
	return &at
}
