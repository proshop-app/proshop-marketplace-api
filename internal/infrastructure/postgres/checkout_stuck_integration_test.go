//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

// stuckPlanSettings — общий план, как у подготовленного запроса процесса; последовательный обход
// запрещён, чтобы на пустой базе план показал, есть ли у условия индекс.
const stuckPlanSettings = `
	SET LOCAL enable_seqscan = off;
	SET LOCAL plan_cache_mode = force_generic_plan`

// Фильтр «зависшие» ведёт от частичного индекса живых ожиданий, а не обходит все группы и покупки.
func TestStuckCheckoutListUsesPartialIndex(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	trace := &checkoutListTrace{}
	traced := checkoutListUnitOfWork(t, pool, trace)
	checkoutListRead(t, traced, trace, checkout.Filter{OrdersOnly: true, StuckBefore: at(15), Limit: 100})
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, stuckPlanSettings); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, prepareCheckoutListPlan+trace.listSQL); err != nil {
		t.Fatal(err)
	}
	args := append([]any{pgx.QueryExecModeSimpleProtocol}, trace.listArgs...)
	rows, err := tx.Query(ctx, explainCheckoutListPlan, args...)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "checkout_groups_stuck_idx") {
		t.Fatalf("фильтр «зависшие» без частичного индекса:\n%s", plan.String())
	}
}
