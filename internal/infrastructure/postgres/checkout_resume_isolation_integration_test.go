//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// breakCheckoutSums — суммы, которые домен не складывает: сохранение покупки падает на пересчёте.
	breakCheckoutSums = `UPDATE checkout_groups
		SET lines = '[{"productId": "p1", "name": "Товар", "quantity": 2, "price": 9000000000000000000}]',
			next_work_at = '2000-01-01'
		WHERE checkout_id = $1`
	// breakCheckoutRow — состояние группы, которое не разбирается.
	breakCheckoutRow = `UPDATE checkout_groups SET order_progress = '"сломано"', next_work_at = '2000-01-02'
		WHERE checkout_id = $1`
	selectNextWork = `SELECT min(next_work_at) FROM checkout_groups WHERE checkout_id = $1`
	// settleCheckoutWork — запись соседнего прохода: срок работы пересчитан, созревшей нет.
	settleCheckoutWork = `UPDATE checkout_groups SET next_work_at = NULL WHERE checkout_id = $1`
	// workPlanSettings — общий план, как у подготовленного запроса процесса.
	workPlanSettings = `
		SET LOCAL enable_seqscan = off;
		SET LOCAL plan_cache_mode = force_generic_plan`
	prepareWorkPlan    = `PREPARE work_candidates AS `
	explainWorkPlan    = `EXPLAIN (COSTS OFF) EXECUTE work_candidates($1, $2, $3, $4)`
	deallocateWorkPlan = `DEALLOCATE work_candidates`
)

// workTrace запоминает запрос выборки созревших групп и его параметры.
type workTrace struct {
	mu   sync.Mutex
	sql  string
	args []any
	// beforeLock выполняется один раз перед блокировкой покупок страницы.
	beforeLock func()
}

// Покупка, которую домен не может сохранить, и строка, которая не разбирается, не останавливают
// проход: остальные покупки получают задачи опроса каждый раз, сломанные откладываются.
func TestResumeIsolatesBrokenPurchases(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	sums, _ := bench.paidPurchase(t, 1)
	row, _ := bench.paidPurchase(t, 2)
	for n := range 3 {
		bench.orderedPurchase(t, 10+n)
	}
	for query, id := range map[string]ids.UUID{breakCheckoutSums: sums, breakCheckoutRow: row} {
		if _, err := bench.pool.Exec(ctx, query, string(id)); err != nil {
			t.Fatal(err)
		}
	}
	bench.clock.Advance(2 * time.Hour)
	if err := bench.service.Resume(ctx); err == nil || !strings.Contains(err.Error(), "money") {
		t.Fatal("сломанные покупки попадают в итог прохода", err)
	}
	for range 2 {
		if err := bench.service.Resume(ctx); err != nil {
			t.Fatal("отложенная покупка не берётся следующими проходами", err)
		}
	}
	bench.assertPollTasks(t, 3*len(orderEventShops))
	for _, id := range []ids.UUID{sums, row} {
		var next time.Time
		if err := bench.pool.QueryRow(ctx, selectNextWork, string(id)).Scan(&next); err != nil {
			t.Fatal(err)
		}
		if !next.After(bench.clock.Now()) {
			t.Fatalf("сломанная покупка %s не отложена: %s", id, next)
		}
	}
}

// Выборка созревших групп идёт по индексу срока в его порядке и под общим планом: при устаревшей
// статистике планировщик не обходит все группы и покупки по ключу.
func TestResumeCandidatesUseNextWorkIndex(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	trace := &workTrace{}
	config := pool.Config()
	config.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	uow := postgres.NewUnitOfWork(traced)
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		_, err := tx.Checkouts().LockWorkDue(ctx, at(0), checkout.WorkCursor{}, 100)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	plan := explainWork(t, pool, trace)
	if !strings.Contains(plan, "checkout_groups_next_work") || strings.Contains(plan, "Sort") ||
		strings.Contains(plan, "checkouts_pkey") || strings.Contains(plan, "checkout_groups_pkey") {
		t.Fatalf("выборка созревших групп не по индексу срока:\n%s", plan)
	}
}

func explainWork(t *testing.T, pool *pgxpool.Pool, trace *workTrace) string {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, workPlanSettings); err != nil {
		t.Fatal(err)
	}
	trace.mu.Lock()
	sql, args := trace.sql, trace.args
	trace.mu.Unlock()
	if sql == "" {
		t.Fatal("выборка созревших групп не выполнялась")
	}
	if _, err := tx.Exec(ctx, prepareWorkPlan+sql); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = tx.Exec(ctx, deallocateWorkPlan) }()
	rows, err := tx.Query(ctx, explainWorkPlan, append([]any{pgx.QueryExecModeSimpleProtocol}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan.String()
}

func (w *workTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	w.mu.Lock()
	defer w.mu.Unlock()
	if strings.Contains(data.SQL, "next_work_at <= $1") {
		w.sql, w.args = data.SQL, data.Args
	}
	if strings.Contains(data.SQL, "SKIP LOCKED") && w.beforeLock != nil {
		hook := w.beforeLock
		w.beforeLock = nil
		hook()
	}
	return ctx
}

func (*workTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Соседний проход сохранил покупку между выборкой кандидатов и блокировкой: страница её не берёт.
func TestLockWorkDueRechecksWorkAfterCandidates(t *testing.T) {
	ctx := context.Background()
	bench := newOrderEventBench(t)
	settled, _ := bench.paidPurchase(t, 800)
	if _, err := bench.pool.Exec(ctx, staleCheckoutWork, string(settled)); err != nil {
		t.Fatal(err)
	}
	trace := &workTrace{beforeLock: func() {
		// Другое соединение, как у соседней реплики: её запись зафиксирована до блокировки.
		if _, err := bench.pool.Exec(ctx, settleCheckoutWork, string(settled)); err != nil {
			t.Error(err)
		}
	}}
	config := bench.pool.Config()
	config.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	var page checkout.WorkPage
	err = do(t, postgres.NewUnitOfWork(traced), func(ctx context.Context, tx ports.Tx) error {
		var err error
		page, err = tx.Checkouts().LockWorkDue(ctx, bench.clock.Now(), checkout.WorkCursor{}, 100)
		return err
	})
	if err != nil || len(page.Due) != 0 {
		t.Fatalf("уже сохранённая покупка взята второй раз: %d %v", len(page.Due), err)
	}
}
