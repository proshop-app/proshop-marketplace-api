//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	app "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	seedSummaryScaleProducts = `
		INSERT INTO catalog_products
			(tenant_id, product_id, revision, deleted, card, price, base_price,
				available, projected_at, version, updated_at)
		SELECT $4, 'p' || lpad(n::text, 6, '0'), 1, false, jsonb_build_object(
			'name', 'Товар p' || lpad(n::text, 6, '0'),
			'visible', true, 'image', '/img.png',
			'marketplace', jsonb_build_object('categoryId', 'c1', 'published', true,
				'attributes', '{"a1": 1}'::jsonb),
			'publication', jsonb_build_object('categoryId', 'c1', 'published', true)
		), 100, 100, 5, $3, 1, $3
		FROM generate_series($1::integer, $2::integer) n`

	seedSummaryScaleVerdicts = `
		INSERT INTO moderation_verdicts (tenant_id, product_id, revision, rules_revision, taxonomy_version, status,
			reasons, decided_by, version, updated_at)
		SELECT tenant_id, product_id, revision, 0, 1, 'allowed', '[]'::jsonb, 'rule', 1, $3 FROM catalog_products
		WHERE tenant_id = 't1' AND product_id BETWEEN $1 AND $2`

	analyzeSummaryScale = `ANALYZE catalog_products; ANALYZE moderation_verdicts;
		ANALYZE store_summary_products; ANALYZE store_summary_changes; ANALYZE store_summary_counts;
		ANALYZE store_summary_ratings`

	summaryMeasuredRelations = `('catalog_products', 'moderation_verdicts', 'store_summary_products',
		'store_summary_changes', 'store_summary_change_cursor', 'store_summary_change_heads',
		'store_summary_change_sources',
		'store_summary_counts', 'store_summary_ratings')`
	summaryReads = `SELECT
		(SELECT COALESCE(sum(seq_tup_read), 0) FROM pg_stat_user_tables
			WHERE relname IN ` + summaryMeasuredRelations + `) +
		(SELECT COALESCE(sum(idx_tup_read), 0) FROM pg_stat_user_indexes
			WHERE relname IN ` + summaryMeasuredRelations + `),
		(SELECT COALESCE(heap_blks_read + heap_blks_hit + idx_blks_read + idx_blks_hit, 0)
			FROM pg_statio_user_tables WHERE relname = 'store_summary_changes'),
		COALESCE((SELECT idx_scan FROM pg_stat_user_indexes WHERE indexrelname = 'store_summary_changes_tenant'), 0),
		COALESCE((SELECT idx_tup_read FROM pg_stat_user_indexes
			WHERE indexrelname = 'store_summary_changes_tenant'), 0)`

	summaryQueuePlan = `EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF, SUMMARY OFF)
		SELECT * FROM store_summary_change_keys('t1', $1, $2, 200)`
	otherStoresQueued = `SELECT count(*) FROM store_summary_changes WHERE tenant_id <> 't1' AND batch_no IS NOT NULL`
)

type storeSummaryCost struct {
	sql         int
	tuples      int64
	queueBlocks int64
	queueReads  int64
	queueTuples int64
}

func TestStoreSummaryPriceDeltaCost(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		sizes      []int
		pinHorizon bool
	}{
		{name: "catalog", sizes: []int{1000, 100000}},
		{name: "concurrent_transaction", sizes: []int{1000, 10000}, pinHorizon: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var costs []storeSummaryCost
			for _, size := range scenario.sizes {
				t.Run(fmt.Sprint(size), func(t *testing.T) {
					costs = append(costs, measureStoreSummaryPriceDelta(t, size, scenario.pinHorizon))
				})
			}
			if len(costs) == 2 {
				small, large := costs[0], costs[1]
				if large.sql != small.sql || large.tuples > small.tuples+5 || large.queueBlocks > small.queueBlocks+12 {
					t.Fatalf("стоимость правки растёт с каталогом: %+v", costs)
				}
			}
		})
	}
}

func TestStoreSummaryPriceDeltaIgnoresOtherStores(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	config := pool.Config()
	config.MaxConns = 1
	trace := &revalidationSQLTrace{}
	config.ConnConfig.Tracer = trace
	measured, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer measured.Close()
	uow := postgres.NewUnitOfWork(measured)
	clock := fake.NewClock(at(12))
	seedSummarySeller(t, uow, clock)
	const products = 1000
	seedSummaryScale(t, measured, products)
	drainSummaryRollup(t, uow)
	if _, err := measured.Exec(ctx, analyzeSummaryScale); err != nil {
		t.Fatal(err)
	}
	service := app.NewService(uow, clock, &fake.IDs{})
	projector := appcatalog.NewProjector(uow, clock, nil)
	refreshSummary(t, service)

	measure := func(product ids.ProductID, kopecks int64, minute int) storeSummaryCost {
		price := summarySnapshot(product, "c1", "", kopecks, 0, 0)
		price.Revision++
		price.At = at(minute)

		before := readSummaryCost(t, measured)
		trace.reset()
		applySummarySnapshot(t, projector, price)
		task := ports.Task{Kind: ports.TaskStoreCard, TenantID: "t1"}
		if err := service.RefreshStoreCard(ctx, task); err != nil {
			t.Fatal(err)
		}
		queries := len(trace.snapshot())
		after := readSummaryCost(t, measured)
		summary, err := service.StoreSummary(ctx, "t1")
		if err != nil || summary.Offers != products || summary.FromPrice == nil ||
			*summary.FromPrice != price.Price.Kopecks() {
			t.Fatalf("цена в сводке не обновилась: %+v, %v", summary, err)
		}
		return storeSummaryCost{
			sql:         queries,
			tuples:      after.tuples - before.tuples,
			queueBlocks: after.queueBlocks - before.queueBlocks,
			queueReads:  after.queueReads - before.queueReads,
			queueTuples: after.queueTuples - before.queueTuples,
		}
	}

	// Для обеих правок берём товары без предыдущих обновлений.
	measure("p000004", 99, 1)
	calm := measure("p000001", 98, 2)
	const otherStores = 50
	writers := openConcurrentSummaryWriters(t, pool, otherStores)
	// Продвигаем курсор, пока чужие записи ещё не зафиксированы.
	measure("p000003", 97, 3)
	var commits errgroup.Group
	for _, tx := range writers {
		commits.Go(func() error {
			if _, err := tx.Exec(ctx, `SELECT pg_stat_force_next_flush()`); err != nil {
				return err
			}
			return tx.Commit(ctx)
		})
	}
	if err := commits.Wait(); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := measured.QueryRow(ctx, otherStoresQueued).Scan(&queued); err != nil || queued != otherStores {
		t.Fatalf("в очереди %d чужих дельт из %d: %v", queued, otherStores, err)
	}
	busy := measure("p000002", 96, 4)

	t.Logf("спокойно: %+v, после %d параллельных записей других магазинов: %+v", calm, otherStores, busy)
	// Размещение новых страниц может добавить обращения к блокам без лишнего обхода очереди.
	if calm.queueReads == 0 || busy.queueReads != calm.queueReads || busy.queueTuples != calm.queueTuples ||
		busy.sql != calm.sql || busy.tuples > calm.tuples || busy.queueBlocks > calm.queueBlocks+2 {
		t.Fatalf("чужие записи изменили стоимость правки: спокойно %+v, после нагрузки %+v", calm, busy)
	}
}

func openConcurrentSummaryWriters(t *testing.T, pool *pgxpool.Pool, stores int) []pgx.Tx {
	t.Helper()
	ctx := context.Background()
	config := pool.Config()
	config.MaxConns = int32(stores)
	concurrent, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	writers := make([]pgx.Tx, stores)
	t.Cleanup(func() {
		for _, tx := range writers {
			if tx != nil {
				_ = tx.Rollback(ctx)
			}
		}
		concurrent.Close()
	})
	var writes errgroup.Group
	for n := range writers {
		writes.Go(func() error {
			tx, err := concurrent.Begin(ctx)
			if err != nil {
				return err
			}
			writers[n] = tx
			tenant := fmt.Sprintf("other-%d", n)
			_, err = tx.Exec(ctx, seedSummaryScaleProducts, 1, 1, at(0), tenant)
			return err
		})
	}
	if err := writes.Wait(); err != nil {
		t.Fatal(err)
	}
	return writers
}

func measureStoreSummaryPriceDelta(t *testing.T, size int, pinHorizon bool) storeSummaryCost {
	t.Helper()
	ctx := context.Background()
	pool := testPool(t)
	config := pool.Config()
	config.MaxConns = 1
	trace := &revalidationSQLTrace{}
	config.ConnConfig.Tracer = trace
	measured, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer measured.Close()
	uow := postgres.NewUnitOfWork(measured)
	clock := fake.NewClock(at(12))
	seedSummarySeller(t, uow, clock)
	if pinHorizon {
		older, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = older.Rollback(ctx) }()
		if _, err := older.Exec(ctx, `SELECT pg_current_xact_id()`); err != nil {
			t.Fatal(err)
		}
	}
	seedSummaryScale(t, measured, size)
	drainSummaryRollup(t, uow)
	if _, err := measured.Exec(ctx, analyzeSummaryScale); err != nil {
		t.Fatal(err)
	}

	service := app.NewService(uow, clock, &fake.IDs{})
	summary := refreshSummary(t, service)
	if summary.Offers != size || summary.FromPrice == nil || *summary.FromPrice != 100 {
		t.Fatalf("некорректный начальный засеянный каталог: %+v", summary)
	}
	before := readSummaryCost(t, measured)
	trace.reset()

	price := summarySnapshot("p000001", "c1", "", 99, 0, 0)
	price.Revision++
	price.At = at(2)
	projector := appcatalog.NewProjector(uow, clock, nil)
	applySummarySnapshot(t, projector, price)
	task := ports.Task{Kind: ports.TaskStoreCard, TenantID: "t1"}
	if err := service.RefreshStoreCard(ctx, task); err != nil {
		t.Fatal(err)
	}
	queries := trace.snapshot()
	if slices.ContainsFunc(queries, func(query string) bool { return strings.Contains(query, "FETCH") }) {
		t.Fatal("сводка открыла курсор по каталогу")
	}
	after := readSummaryCost(t, measured)
	cost := storeSummaryCost{
		sql:         len(queries),
		tuples:      after.tuples - before.tuples,
		queueBlocks: after.queueBlocks - before.queueBlocks,
	}
	message := "%d товаров: %d SQL, %d строк и записей индексов, %d обращений к блокам очереди"
	t.Logf(message, size, cost.sql, cost.tuples, cost.queueBlocks)
	if cost.sql > 25 || cost.tuples == 0 || cost.tuples > 64 || cost.queueBlocks == 0 || cost.queueBlocks > 64 {
		t.Fatalf("неадресная правка: %+v", cost)
	}
	summary, err = service.StoreSummary(ctx, "t1")
	if err != nil || summary.Offers != size || summary.FromPrice == nil || *summary.FromPrice != 99 {
		t.Fatalf("цена в сводке не обновилась: %+v, %v", summary, err)
	}
	if pinHorizon {
		next := summarySnapshot(price.ProductID, "c1", "", 98, 0, 0)
		next.Revision = price.Revision + 1
		next.At = at(3)
		applySummarySnapshot(t, projector, next)
		assertSummaryQueuePlan(t, measured)
	}
	return cost
}

func assertSummaryQueuePlan(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var position int64
	var id int64
	query := `SELECT batch_no, id FROM store_summary_change_cursor WHERE tenant_id = 't1'`
	if err := pool.QueryRow(ctx, query).Scan(&position, &id); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, summaryQueuePlan, position, id)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(lines, "\n")
	t.Logf("План чтения одной новой дельты после обработки очереди:\n%s", plan)
	if !strings.Contains(plan, "using store_summary_changes_tenant") ||
		!strings.Contains(plan, `ROW(batch_no, id) > ROW(`) || !strings.Contains(plan, "rows=1 loops=1") {
		t.Fatalf("граница обработанной истории отсутствует в индексном чтении:\n%s", plan)
	}
}

func seedSummaryScale(t *testing.T, pool *pgxpool.Pool, size int) {
	t.Helper()
	ctx := context.Background()
	const batch = 5000
	for first := 1; first <= size; first += batch {
		last := min(first+batch-1, size)
		if _, err := pool.Exec(ctx, seedSummaryScaleProducts, first, last, at(0), "t1"); err != nil {
			t.Fatal(err)
		}
		low, high := fmt.Sprintf("p%06d", first), fmt.Sprintf("p%06d", last)
		if _, err := pool.Exec(ctx, seedSummaryScaleVerdicts, low, high, at(0)); err != nil {
			t.Fatal(err)
		}
	}
}

func drainSummaryRollup(t *testing.T, uow *postgres.UnitOfWork) {
	t.Helper()
	for more := true; more; {
		err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
			if err := tx.StoreCards().Lock(ctx, "t1"); err != nil {
				return err
			}
			facts, err := tx.StoreCards().Rollup(ctx, "t1", true, []string{"c1", "c2"})
			more = facts.More
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func readSummaryCost(t *testing.T, pool *pgxpool.Pool) storeSummaryCost {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `SELECT pg_stat_force_next_flush()`); err != nil {
		t.Fatal(err)
	}
	var cost storeSummaryCost
	err := pool.QueryRow(ctx, summaryReads).Scan(&cost.tuples, &cost.queueBlocks, &cost.queueReads, &cost.queueTuples)
	if err != nil {
		t.Fatal(err)
	}
	return cost
}
