//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	// rejectCheckoutWrite — база отвергает запись одной покупки, домен её не отличает от здоровой.
	rejectCheckoutWrite = `CREATE FUNCTION reject_checkout_write() RETURNS trigger AS $$
		BEGIN
			IF NEW.id = '%s' THEN RAISE EXCEPTION 'запись покупки отвергнута'; END IF;
			RETURN NEW;
		END $$ LANGUAGE plpgsql;
		CREATE TRIGGER reject_checkout_write BEFORE UPDATE ON checkouts
			FOR EACH ROW EXECUTE FUNCTION reject_checkout_write()`
	selectDeferredWork = `SELECT min(next_work_at), bool_or(work_deferred_at IS NOT NULL)
		FROM checkout_groups WHERE checkout_id = $1`
	farFutureWork       = `UPDATE checkout_groups SET next_work_at = '2100-01-01' WHERE checkout_id = $1`
	farFutureAnchorWork = `UPDATE checkout_groups SET next_work_at = '2100-01-01'
		WHERE checkout_id = $1 AND tenant_id = 't1'`
	expireAllReconciliation = `UPDATE checkout_groups SET reconcile_until = '2000-01-01' WHERE checkout_id = $1`
	lockGroupRows           = `SELECT 1 FROM checkout_groups WHERE checkout_id = $1 FOR NO KEY UPDATE`
	draftCheckout           = `UPDATE checkouts SET status = 'draft', order_context = '{}' WHERE id = $1`
	markDeferredOnly        = `UPDATE checkout_groups SET work_deferred_at = $2 WHERE checkout_id = $1`
	deferWorkWaiting        = `SELECT cardinality(pg_blocking_pids($1)) > 0`
)

// lockTrace запоминает запрос блокировки страницы и его параметры.
type lockTrace struct {
	sql  string
	args []any
}

type deferWorkTrace struct {
	started chan uint32
}

// Сохранение страницы упало из-за одной покупки: страница разбирается по одной, остальные
// сохраняются, виновная и неразобравшаяся строка откладываются и видны оператору в «зависших»;
// следующий проход их не берёт.
func TestResumeDefersPurchaseThatCannotBeSaved(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	bad, _ := bench.paidPurchase(t, 900)
	row, _ := bench.paidPurchase(t, 901)
	var healthy []ids.UUID
	for n := range 2 {
		id, _ := bench.paidPurchase(t, 910+n)
		healthy = append(healthy, id)
	}
	if _, err := bench.pool.Exec(ctx, breakCheckoutRow, string(row)); err != nil {
		t.Fatal(err)
	}
	if _, err := bench.pool.Exec(ctx, fmt.Sprintf(rejectCheckoutWrite, bad)); err != nil {
		t.Fatal(err)
	}
	for _, id := range append(healthy, bad) {
		if _, err := bench.pool.Exec(ctx, staleCheckoutWork, string(id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := bench.pool.Exec(ctx, farFutureAnchorWork, string(bad)); err != nil {
		t.Fatal(err)
	}
	versions := bench.checkoutVersions(t, healthy)
	if err := bench.service.Resume(ctx); err == nil {
		t.Fatal("отложенные покупки попадают в итог прохода")
	}
	if got := bench.checkoutVersions(t, healthy); got[0] != versions[0]+1 || got[1] != versions[1]+1 {
		t.Fatalf("здоровые покупки сохранены по одной: %v, были %v", got, versions)
	}
	for _, id := range []ids.UUID{bad, row} {
		var next time.Time
		var deferred bool
		if err := bench.pool.QueryRow(ctx, selectDeferredWork, string(id)).Scan(&next, &deferred); err != nil {
			t.Fatal(err)
		}
		if !next.After(bench.clock.Now()) || !deferred {
			t.Fatalf("покупка %s не отложена: %s %v", id, next, deferred)
		}
	}
	stuck, err := bench.service.OperatorList(ctx, checkout.ListCursor{}, true)
	if err != nil || len(stuck.Items) != 1 || !containsCheckout(stuck.Items, bad) || len(stuck.Issues) != 4 {
		t.Fatalf("отложенные покупки видны оператору в «зависших»: %+v, %v", stuck, err)
	}
	for _, issue := range stuck.Issues {
		if issue.CheckoutID != row || issue.TenantID == "" {
			t.Fatalf("ошибка группы отложенной покупки: %+v", issue)
		}
	}
	if err := bench.service.Resume(ctx); err != nil {
		t.Fatal("отложенные покупки не возвращаются каждый проход", err)
	}
}

// Страница групп полная — есть следующая; курсор продолжает с последней группы.
func TestLockWorkDueContinuesByCursor(t *testing.T) {
	bench := newOrderEventBench(t)
	for n := range 3 {
		bench.orderedPurchase(t, 920+n)
	}
	bench.clock.Advance(2 * time.Hour)
	seen := map[ids.UUID]bool{}
	var cursor checkout.WorkCursor
	pages := 0
	for {
		var page checkout.WorkPage
		err := do(t, bench.uow, func(ctx context.Context, tx ports.Tx) error {
			var err error
			page, err = tx.Checkouts().LockWorkDue(ctx, bench.clock.Now(), cursor, 2)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, c := range page.Due {
			seen[c.ID()] = true
		}
		if !page.More {
			break
		}
		cursor = page.Next
	}
	if len(seen) != 3 || pages < 3 {
		t.Fatalf("три покупки за несколько страниц: покупок %d, страниц %d", len(seen), pages)
	}
}

// Отложить — не приблизить: будущий срок работы не сдвигается назад.
func TestDeferWorkKeepsLaterWork(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	id, _ := bench.paidPurchase(t, 930)
	if _, err := bench.pool.Exec(ctx, farFutureWork, string(id)); err != nil {
		t.Fatal(err)
	}
	err := do(t, bench.uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().DeferWork(ctx, []ids.UUID{id}, bench.clock.Now().Add(time.Minute), bench.clock.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	var next time.Time
	var deferred bool
	if err := bench.pool.QueryRow(ctx, selectDeferredWork, string(id)).Scan(&next, &deferred); err != nil {
		t.Fatal(err)
	}
	if next.Year() != 2100 {
		t.Fatalf("будущий срок сдвинут назад: %s", next)
	}
}

func TestDeferWorkWaitsForPaidCheckoutAndKeepsWorkCleared(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	id := bench.orderedPurchase(t, 931).ID()
	now := bench.clock.Now()
	if _, err := bench.pool.Exec(ctx, staleCheckoutWork, string(id)); err != nil {
		t.Fatal(err)
	}
	trace := &deferWorkTrace{started: make(chan uint32, 2)}
	deferrer := checkoutListUnitOfWork(t, bench.pool, trace)
	finished := make(chan error, 1)

	err := bench.uow.Do(ctx, func(saveCtx context.Context, tx ports.Tx) error {
		purchase, err := tx.Checkouts().GetForUpdate(saveCtx, id)
		if err != nil {
			return err
		}
		go func() {
			finished <- deferrer.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				return tx.Checkouts().DeferWork(ctx, []ids.UUID{id}, now.Add(time.Hour), now)
			})
		}()
		if err := waitForDeferWorkLock(saveCtx, bench.pool, trace.started, finished); err != nil {
			return err
		}
		changed, err := purchase.Pay(now)
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("оплата не изменила покупку")
		}
		return tx.Checkouts().Update(saveCtx, purchase)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}

	var next *time.Time
	var deferred bool
	if err := bench.pool.QueryRow(ctx, selectDeferredWork, string(id)).Scan(&next, &deferred); err != nil {
		t.Fatal(err)
	}
	state, err := bench.service.Get(ctx, buyerA, id)
	if err != nil || state.Status != checkout.StatusPaid || next != nil || deferred {
		t.Fatalf(
			"после оплаты работа появилась снова: статус=%s, срок=%v, флаг=%t, %v",
			state.Status,
			next,
			deferred,
			err,
		)
	}
}

func waitForDeferWorkLock(
	ctx context.Context,
	pool *pgxpool.Pool,
	started <-chan uint32,
	finished <-chan error,
) error {
	var pid uint32
	select {
	case pid = <-started:
	case err := <-finished:
		return errors.Join(errors.New("отсрочка завершилась без блокировки"), err)
	case <-ctx.Done():
		return ctx.Err()
	}
	return waitForCheckoutWriteBlock(ctx, pool, pid, finished)
}

func waitForCheckoutWriteBlock(ctx context.Context, pool *pgxpool.Pool, pid uint32, finished <-chan error) error {
	for {
		select {
		case err := <-finished:
			return errors.Join(errors.New("запись покупки завершилась без ожидания параллельной записи"), err)
		default:
		}
		var blocked bool
		err := pool.QueryRow(ctx, deferWorkWaiting, pid).Scan(&blocked)
		if err != nil || blocked {
			return err
		}
	}
}

func (trace *deferWorkTrace) TraceQueryStart(
	ctx context.Context,
	conn *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, "checkouts") || strings.Contains(data.SQL, "checkout_groups") {
		trace.started <- conn.PgConn().PID()
	}
	return ctx
}

func (*deferWorkTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Запрос блокировки страницы идёт только по ключу покупок — и в своём, и в общем плане: он не
// разворачивается от созревших групп при устаревшей статистике.
func TestResumeLockUsesCheckoutKeyOnly(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	bench.orderedPurchase(t, 940)
	bench.clock.Advance(2 * time.Hour)
	trace := &lockTrace{}
	config := bench.pool.Config()
	config.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	err = do(t, postgres.NewUnitOfWork(traced), func(ctx context.Context, tx ports.Tx) error {
		_, err := tx.Checkouts().LockWorkDue(ctx, bench.clock.Now(), checkout.WorkCursor{}, 100)
		return err
	})
	if err != nil || trace.sql == "" {
		t.Fatal("запрос блокировки не выполнялся", err)
	}
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		plan := explainLock(t, bench.pool, trace, mode)
		if strings.Contains(plan, "checkout_groups") || !strings.Contains(plan, "checkouts_pkey") {
			t.Fatalf("%s: блокировка страницы не только по ключу покупок:\n%s", mode, plan)
		}
	}
}

func (l *lockTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SKIP LOCKED") {
		l.sql, l.args = data.SQL, data.Args
	}
	return ctx
}

func (*lockTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func explainLock(t *testing.T, pool *pgxpool.Pool, trace *lockTrace, mode string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off; SET LOCAL plan_cache_mode = "+mode); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "PREPARE lock_page AS "+trace.sql); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = tx.Exec(ctx, "DEALLOCATE lock_page") }()
	rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) EXECUTE lock_page($1)", append(
		[]any{pgx.QueryExecModeSimpleProtocol},
		trace.args...,
	)...)
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
	return plan.String()
}

func containsCheckout(states []checkout.State, id ids.UUID) bool {
	for _, state := range states {
		if state.ID == id {
			return true
		}
	}
	return false
}

// Снятие истёкших сроков сверки не ждёт и не сцепляется с записью покупки: занятую группу пропускает.
func TestExpireReconciliationSkipsLockedGroups(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	id, _ := bench.paidPurchase(t, 950)
	if _, err := bench.pool.Exec(ctx, expireAllReconciliation, string(id)); err != nil {
		t.Fatal(err)
	}
	tx, err := bench.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, lockGroupRows, string(id)); err != nil {
		t.Fatal(err)
	}
	quick, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err = do(t, bench.uow, func(_ context.Context, tx ports.Tx) error {
		return tx.Checkouts().ExpireReconciliation(quick, bench.clock.Now())
	})
	if err != nil {
		t.Fatal("снятие сроков ждёт занятую группу", err)
	}
}

// Отложенная работа снимается первым удачным сохранением покупки, даже у групп, чьи поля не
// изменились (срок совпал с пересчитанным): оператор не видит покупку зависшей.
func TestSavedPurchaseClearsDeferredWork(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	id, orders := bench.paidPurchase(t, 960)
	if _, err := bench.pool.Exec(ctx, markDeferredOnly, string(id), bench.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := bench.service.ApplyOrderEvent(ctx, assembledEvent(id, "t1", orders["t1"], 1)); err != nil {
		t.Fatal(err)
	}
	var next *time.Time
	var deferred bool
	if err := bench.pool.QueryRow(ctx, selectDeferredWork, string(id)).Scan(&next, &deferred); err != nil {
		t.Fatal(err)
	}
	if deferred {
		t.Fatal("сохранение покупки не сняло признак отложенной работы")
	}
}

// Черновик без оформленных заказов страница восстановления не берёт, даже со сроком работы групп.
func TestLockWorkDueSkipsDrafts(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	id, _ := bench.paidPurchase(t, 970)
	for _, query := range []string{staleCheckoutWork, draftCheckout} {
		if _, err := bench.pool.Exec(ctx, query, string(id)); err != nil {
			t.Fatal(err)
		}
	}
	var page checkout.WorkPage
	err := do(t, bench.uow, func(ctx context.Context, tx ports.Tx) error {
		var err error
		page, err = tx.Checkouts().LockWorkDue(ctx, bench.clock.Now(), checkout.WorkCursor{}, 100)
		return err
	})
	if err != nil || len(page.Due) != 0 {
		t.Fatalf("черновик взят страницей: %d %v", len(page.Due), err)
	}
}

// Отложенная покупка, которую следующий проход разобрал без изменений, сохраняется и признак снимается.
func TestResumeClearsDeferredWorkWithoutChange(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	c := bench.orderedPurchase(t, 980)
	err := do(t, bench.uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().DeferWork(ctx, []ids.UUID{c.ID()}, bench.clock.Now().Add(time.Hour), bench.clock.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	bench.clock.Advance(2 * time.Hour)
	if err := bench.service.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	var next *time.Time
	var deferred bool
	if err := bench.pool.QueryRow(ctx, selectDeferredWork, string(c.ID())).Scan(&next, &deferred); err != nil {
		t.Fatal(err)
	}
	if deferred {
		t.Fatal("разобранная покупка осталась отложенной")
	}
}
