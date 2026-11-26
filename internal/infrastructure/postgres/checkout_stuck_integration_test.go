//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const (
	seedStuckCheckouts = `
		INSERT INTO checkouts (id, buyer_id, idempotency_key, status, goods_total, delivery_total, total,
			version, created_at, updated_at)
		SELECT lpad(n::text, 32, '0')::uuid, $3, 'stuck-' || n,
			CASE WHEN $5 THEN 'confirmed' ELSE 'awaiting_payment' END, 0, 0, 0, 1, $4, $4
		FROM generate_series($1::integer, $2::integer) n`
	seedStuckGroups = `
		INSERT INTO checkout_groups (checkout_id, tenant_id, external_checkout_id, lines, goods,
			order_progress, work_deferred_at, stuck_at)
		SELECT lpad(n::text, 32, '0')::uuid, 't' || shop, 'stuck-' || n || '-' || shop, '[]', 0,
			jsonb_build_object('Status', 'awaiting_payment', 'AwaitingSince', $3::timestamptz),
			CASE WHEN $4 THEN $3 END,
			CASE WHEN shop = 1 AND NOT $7 THEN
				CASE WHEN $4 THEN '0001-01-01 00:00:00+00'::timestamptz ELSE $3 END
			END
		FROM generate_series($1::integer, $2::integer) n
		CROSS JOIN generate_series($5::integer, $6::integer) shop`
	analyzeStuckCheckouts = `ANALYZE checkouts; ANALYZE checkout_groups`
	stuckPreparePlan      = `PREPARE stuck_page AS `
	stuckExplainPlan      = `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE stuck_page($1, $2)`
	stuckExplainAfterPlan = `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE stuck_page($1, $2, $3, $4)`
	stuckGenericPlan      = `
		SET LOCAL plan_cache_mode = force_generic_plan;
		SET LOCAL jit = on;
		SET LOCAL jit_above_cost = 100000`
	stuckReleasePlan = `DEALLOCATE stuck_page`
)

type stuckPagePlan struct {
	readPlanExplain
	JIT json.RawMessage
}

// Обе страницы идут по ключам видимых покупок при общем плане с включённым JIT.
func TestStuckCheckoutPagesUseBoundedIndexScan(t *testing.T) {
	pool := testPool(t)
	trace := &checkoutListTrace{}
	uow := checkoutListUnitOfWork(t, pool, trace)
	seedBuyer(t, uow, buyerA, "79251234567")
	seedStuckWaits(t, pool, 130000, 130299, at(0), false, true)
	seedStuckWaits(t, pool, 130300, 130599, at(0), false, false)

	filter := checkout.Filter{OrdersOnly: true, StuckBefore: at(15), Limit: 100}
	baseline := make([]int, 2)
	first := checkoutListRead(t, uow, trace, filter)
	if len(first) != 100 {
		t.Fatalf("первая страница: %d покупок", len(first))
	}
	queries := []checkoutListTrace{*trace}
	baseline[0] = stuckPageBuffers(t, pool, trace, false)
	immediate := time.Time{}
	after := filter
	after.AfterID, after.AfterStuckAt = first[len(first)-1].ID(), &immediate
	second := checkoutListRead(t, uow, trace, after)
	if len(second) != 100 || second[0].ID() <= after.AfterID {
		t.Fatalf("страница после курсора %s: %d покупок", after.AfterID, len(second))
	}
	queries = append(queries, *trace)
	baseline[1] = stuckPageBuffers(t, pool, trace, false)

	ctx := context.Background()
	// Первые 200 видимых покупок получают по 100 групп, перед ними 25 тыс. невидимых.
	// Остальные группы образуют будущие ожидания. Всего в базе ровно миллион групп.
	if _, err := pool.Exec(ctx, seedStuckGroups, 130000, 130199, at(0), true, 2, 100, false); err != nil {
		t.Fatal(err)
	}
	seedStuckWaits(t, pool, 100000, 124999, at(0), true, true)
	for first := 200000; first < 219000; first += 1000 {
		seedStuckWaits(t, pool, first, first+999, at(30), false, false)
		if _, err := pool.Exec(ctx, seedStuckGroups, first, first+999, at(30), false, 2, 50, false); err != nil {
			t.Fatal(err)
		}
	}
	seedStuckWaits(t, pool, 219000, 223599, at(30), false, false)
	if _, err := pool.Exec(ctx, analyzeStuckCheckouts); err != nil {
		t.Fatal(err)
	}
	for i, query := range queries {
		buffers := stuckPageBuffers(t, pool, &query, true)
		// Допуск покрывает изменение глубины B-tree и стоимости поиска по ключу покупки.
		if buffers > baseline[i]+256 || buffers > 1600 {
			t.Errorf("страница %d, буферов до и после роста ожиданий: %d → %d", i+1, baseline[i], buffers)
		}
	}
}

func TestStuckCheckoutBackfill(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	purchases := seedCheckoutList(t, uow)
	seedStuckWaits(t, pool, 1, 300, at(30), false, false)
	seedStuckWaits(t, pool, 301, 310, at(0), true, true)
	if err := postgres.MigrateDownTo(ctx, pool, 73); err != nil {
		t.Fatal(err)
	}
	deferred := purchases[2].ID()
	if _, err := pool.Exec(ctx, markDeferredOnly, string(deferred), at(1)); err != nil {
		t.Fatal(err)
	}
	if err := migrateUpTo(ctx, pool, 74); err != nil {
		t.Fatal(err)
	}

	trace := &checkoutListTrace{}
	traced := checkoutListUnitOfWork(t, pool, trace)
	page := checkoutListRead(t, traced, trace, checkout.Filter{OrdersOnly: true, Limit: 10, StuckBefore: at(15)})
	if len(page) != 2 || page[0].ID() != deferred || page[1].ID() != purchases[3].ID() {
		t.Fatalf("миграция не восстановила отложенную покупку и давнее ожидание: %+v", page)
	}
}

func TestStuckCheckoutTracksReconciliationAndClearsAfterSave(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	purchase := seedCheckoutList(t, uow)[3]
	trace := &checkoutListTrace{}
	traced := checkoutListUnitOfWork(t, pool, trace)
	filter := checkout.Filter{StuckBefore: at(5), Limit: 10, OrdersOnly: true}
	if page := checkoutListRead(t, traced, trace, filter); len(page) != 0 {
		t.Fatalf("ожидание оплаты ещё не началось: %+v", page)
	}
	if changed, err := purchase.Cancel(at(11)); err != nil || !changed {
		t.Fatalf("запрос отмены: %v, %v", changed, err)
	}
	save := func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Update(ctx, purchase)
	}
	if err := do(t, uow, save); err != nil {
		t.Fatal(err)
	}
	if page := checkoutListRead(t, traced, trace, filter); len(page) != 1 || page[0].ID() != purchase.ID() {
		t.Fatalf("покупка со сверкой отмены должна появиться сразу: %+v", page)
	}
	for _, group := range purchase.Groups() {
		changed, err := purchase.CompleteCancellation(group.TenantID, group.OrderID, at(12))
		if err != nil || !changed {
			t.Fatalf("подтверждение отмены: %v, %v", changed, err)
		}
	}
	if err := do(t, uow, save); err != nil {
		t.Fatal(err)
	}
	if page := checkoutListRead(t, traced, trace, filter); len(page) != 0 {
		t.Fatalf("покупка со снятой меткой осталась в зависших: %+v", page)
	}
}

func seedStuckWaits(t *testing.T, pool *pgxpool.Pool, first, last int, when time.Time, hidden, deferred bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, seedStuckCheckouts, first, last, string(buyerA), when, hidden); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, seedStuckGroups, first, last, when, deferred, 1, 1, hidden); err != nil {
		t.Fatal(err)
	}
}

func stuckPageBuffers(t *testing.T, pool *pgxpool.Pool, trace *checkoutListTrace, requireIndex bool) int {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, stuckGenericPlan); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, stuckPreparePlan+trace.listSQL); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = tx.Exec(ctx, stuckReleasePlan) }()

	args := append([]any{pgx.QueryExecModeSimpleProtocol}, trace.listArgs...)
	var explain string
	switch len(trace.listArgs) {
	case 2:
		explain = stuckExplainPlan
	case 4:
		explain = stuckExplainAfterPlan
	default:
		t.Fatalf("неожиданное число параметров страницы: %d", len(trace.listArgs))
	}
	var raw []byte
	if err := tx.QueryRow(ctx, explain, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var result []stuckPagePlan
	if err := json.Unmarshal(raw, &result); err != nil || len(result) != 1 {
		t.Fatalf("план: %s, ошибка: %v", raw, err)
	}
	plan := result[0].Plan
	indexes := map[string]bool{}
	collectReadPlanIndexes(plan, indexes)
	indexed := len(indexes) == 2 && indexes["checkout_groups_stuck_cursor_idx"] && indexes["checkouts_pkey"]
	if plan.ActualRows != 100 || (requireIndex && !indexed) || len(result[0].JIT) > 0 {
		t.Fatalf("ожидалась индексная страница из 100 покупок: %s", raw)
	}
	buffers := plan.SharedHit + plan.SharedRead
	t.Logf("страница (%d параметра): %d буферов, %.2f мс", len(trace.listArgs), buffers, result[0].ExecutionTime)
	return buffers
}
