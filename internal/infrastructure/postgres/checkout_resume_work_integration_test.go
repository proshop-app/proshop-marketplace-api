//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// staleCheckoutWork — срок работы, записанный прежней версией: созревшей работы у групп нет.
	staleCheckoutWork = `UPDATE checkout_groups SET next_work_at = '2000-01-01' WHERE checkout_id = $1`
	// requireCancellation — группа оставлена с возможным заказом: нужна отмена у магазина.
	requireCancellation = `UPDATE checkout_groups
		SET order_progress = order_progress || '{"Abandoned": true, "ReconciliationRequired": true}',
			next_work_at = '2000-01-01'
		WHERE checkout_id = $1 AND tenant_id = 't1'`
	selectResumedWork = `SELECT c.version, count(*) FILTER (WHERE g.next_work_at <= $2)
		FROM checkouts c JOIN checkout_groups g ON g.checkout_id = c.id
		WHERE c.id = $1 GROUP BY c.version`
	countCancelTasks = `SELECT count(*) FROM outbox WHERE kind = $1`
	lockOneCheckout  = `SELECT 1 FROM checkouts WHERE id = $1 FOR NO KEY UPDATE`
	groupRowVersions = `SELECT tenant_id, xmin::text FROM checkout_groups WHERE checkout_id = $1 ORDER BY tenant_id`
)

// Покупки с устаревшим сроком работы сохраняются проходом (срок пересчитан, версия выросла), и
// параллельные проходы не конфликтуют на них версией: покупку берёт один проход.
func TestResumeSavesSettledPurchasesWithoutConflict(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	var purchases []ids.UUID
	for n := range 8 {
		id, _ := bench.paidPurchase(t, 300+n)
		if _, err := bench.pool.Exec(ctx, staleCheckoutWork, string(id)); err != nil {
			t.Fatal(err)
		}
		purchases = append(purchases, id)
	}
	versions := bench.checkoutVersions(t, purchases)
	failures := make(chan error, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures <- bench.service.Resume(ctx)
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal("параллельный проход не конфликтует на изменённой покупке", err)
		}
	}
	for i, id := range purchases {
		var version int64
		var due int
		if err := bench.pool.QueryRow(ctx, selectResumedWork, string(id), bench.clock.Now()).Scan(&version, &due); err != nil {
			t.Fatal(err)
		}
		if version != versions[i]+1 || due != 0 {
			t.Fatalf("покупка %s: версия %d (была %d), созревших групп %d", id, version, versions[i], due)
		}
	}
}

// Группе, оставленной с возможным заказом, проход ставит отмену у магазина и отмечает её.
func TestResumeQueuesCancellation(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	c := bench.orderedPurchase(t, 400)
	if _, err := bench.pool.Exec(ctx, requireCancellation, string(c.ID())); err != nil {
		t.Fatal(err)
	}
	if err := bench.service.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	var tasks int
	if err := bench.pool.QueryRow(ctx, countCancelTasks, appcheckout.TaskCancelOrder).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	state, err := bench.service.Get(ctx, buyerA, c.ID())
	if err != nil || tasks != 1 || !state.Groups[0].CancellationQueued {
		t.Fatalf("отмена поставлена и отмечена: задач %d, %v %+v", tasks, err, state.Groups[0])
	}
}

// Покупку, занятую другой записью, проход пропускает, а не ждёт её до срока запроса и не
// перезаписывает: занятой покупке нужно сохранение (устаревший срок работы).
func TestResumeSkipsLockedPurchase(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	locked, _ := bench.paidPurchase(t, 500)
	if _, err := bench.pool.Exec(ctx, staleCheckoutWork, string(locked)); err != nil {
		t.Fatal(err)
	}
	bench.orderedPurchase(t, 501)
	bench.clock.Advance(2 * time.Hour)
	version := bench.checkoutVersions(t, []ids.UUID{locked})[0]
	tx, err := bench.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, lockOneCheckout, string(locked)); err != nil {
		t.Fatal(err)
	}
	quick, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := bench.service.Resume(quick); err != nil {
		t.Fatal("занятая покупка не держит проход", err)
	}
	bench.assertPollTasks(t, len(orderEventShops))
	_ = tx.Rollback(ctx)
	if got := bench.checkoutVersions(t, []ids.UUID{locked})[0]; got != version {
		t.Fatalf("занятая покупка перезаписана проходом: версия %d, была %d", got, version)
	}
}

// Событие одного магазина переписывает строку только своей группы: остальные строки покупки
// не получают новых версий.
func TestOrderEventRewritesOnlyItsGroup(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	id, orders := bench.paidPurchase(t, 600)
	before := bench.groupRowVersions(t, id)
	if err := bench.service.ApplyOrderEvent(ctx, assembledEvent(id, "t1", orders["t1"], 1)); err != nil {
		t.Fatal(err)
	}
	after := bench.groupRowVersions(t, id)
	for _, tenant := range orderEventShops {
		changed := before[tenant] != after[tenant]
		if changed != (tenant == "t1") {
			t.Fatalf("строка группы %s переписана: %v (до %s, после %s)", tenant, changed, before[tenant], after[tenant])
		}
	}
}

// Действие покупателя читает покупку под блокировкой строки, как и событие магазина.
func TestBuyerActionReadsPurchaseUnderLock(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	c := bench.orderedPurchase(t, 700)
	bench.counter.reset()
	bench.counter.remember(true)
	if _, err := bench.service.Pay(ctx, buyerA, c.ID()); err != nil {
		t.Fatal(err)
	}
	locked := false
	for _, sql := range bench.counter.statements() {
		if strings.Contains(sql, "FROM checkouts") && strings.Contains(sql, "FOR NO KEY UPDATE") {
			locked = true
		}
	}
	if !locked {
		t.Fatal("действие покупателя читает покупку без блокировки строки")
	}
}

func (b *orderEventBench) checkoutVersions(t *testing.T, purchases []ids.UUID) []int64 {
	t.Helper()
	versions := make([]int64, 0, len(purchases))
	for _, id := range purchases {
		var version int64
		var due int
		err := b.pool.QueryRow(context.Background(), selectResumedWork, string(id), b.clock.Now()).Scan(&version, &due)
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, version)
	}
	return versions
}

func (b *orderEventBench) groupRowVersions(t *testing.T, id ids.UUID) map[ids.TenantID]string {
	t.Helper()
	rows, err := b.pool.Query(context.Background(), groupRowVersions, string(id))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	versions := map[ids.TenantID]string{}
	for rows.Next() {
		var tenant, xmin string
		if err := rows.Scan(&tenant, &xmin); err != nil {
			t.Fatal(err)
		}
		versions[ids.TenantID(tenant)] = xmin
	}
	return versions
}
