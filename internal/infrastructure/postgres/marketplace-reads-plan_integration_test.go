//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	readPlanSeedGenerations = `INSERT INTO store_card_cache_versions (tenant_id, version)
 SELECT 't' || n::text, 1 FROM generate_series(1, 1000) n`
	readPlanSeedProducts = `INSERT INTO catalog_products
 (tenant_id, product_id, revision, deleted, card, price, available, projected_at, version, updated_at)
 SELECT tenant_id, 'p' || lpad(n::text, 6, '0'), 1, false,
 jsonb_build_object('name', 'Товар', 'visible', true, 'assets', '[]'::jsonb,
 'variantsId', 'g' || (n / 3)::text, 'publication', jsonb_build_object('published', true)),
 100, 1, $1, 1, $1 FROM tenants
 CROSS JOIN LATERAL generate_series(1, CASE WHEN tenant_id = 't001000' THEN 2100 ELSE 100 END) n`
	readPlanSeedCases = `INSERT INTO moderation_cases
 (id, tenant_id, product_id, revision, source, status, reasons, note, opened_at, version)
 SELECT md5(n::text)::uuid, 't' || lpad(((n - 1) / 100 + 1)::text, 6, '0'),
 'p' || lpad(((n - 1) % 100 + 1)::text, 6, '0'), 1, 'rule', 'allowed', '[]'::jsonb, '', $1, 1
 FROM generate_series(1, 100000) n`
	readPlanSeedModerationDecisions = `INSERT INTO moderation_decisions
 (id, case_id, operator_id, operator_name, action, reason, note, revision, at)
 SELECT md5(c.id::text || n::text)::uuid, c.id, 'o1', 'Оператор', 'allow', '', 'Решение', 1, $1
 FROM moderation_cases c CROSS JOIN generate_series(1, 2) n`
	readPlanSeedCampaigns = `INSERT INTO promotion_campaigns
 (id, tenant_id, kind, placement_code, position, category_id, date_from, date_to, price,
 status, version, state, created_at, updated_at, product_id)
 SELECT md5(n::text)::uuid, 't' || lpad(((n - 1) / 100 + 1)::text, 6, '0'),
 'sponsored_slot', CASE WHEN n % 2 = 0 THEN 'category_slot' ELSE 'search_slot' END,
 1, 'c' || (n % 97)::text, '2026-11-02'::date, '2026-11-29'::date, 100,
 CASE WHEN n % 100 = 0 OR n % 100 = 1 THEN 'active'
 WHEN n % 100 = 2 THEN 'approved' WHEN n % 100 = 3 THEN 'review' ELSE 'finished' END,
 1, '{}'::jsonb, $1, $1, 'p' || lpad(((n - 1) % 100 + 1)::text, 6, '0')
 FROM generate_series(1, 100000) n`
	readPlanSeedPromotionDecisions = `INSERT INTO promotion_decisions (campaign_id, at, actor, action)
 SELECT id, $1, 'system', 'activate' FROM promotion_campaigns CROSS JOIN generate_series(1, 2) n`
	readPlanAnalyze = `ANALYZE tenants; ANALYZE participants; ANALYZE catalog_products;
 ANALYZE moderation_cases; ANALYZE moderation_decisions; ANALYZE promotion_campaigns;
 ANALYZE promotion_decisions`
)

type readPlanTrace struct {
	match string
	sql   string
	args  []any
}

type readPlanNode struct {
	IndexName  string         `json:"Index Name"`
	ActualRows int            `json:"Actual Rows"`
	SharedHit  int            `json:"Shared Hit Blocks"`
	SharedRead int            `json:"Shared Read Blocks"`
	Plans      []readPlanNode `json:"Plans"`
}

type readPlanExplain struct {
	Plan          readPlanNode `json:"Plan"`
	ExecutionTime float64      `json:"Execution Time"`
}

func (trace *readPlanTrace) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, trace.match) {
		trace.sql, trace.args = data.SQL, data.Args
	}
	return ctx
}

func (*readPlanTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestStoreCardGlobalGenerationPlan(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	if _, err := pool.Exec(ctx, readPlanSeedGenerations); err != nil {
		t.Fatal(err)
	}
	trace := &readPlanTrace{match: "FROM store_card_cache_generation"}
	config := pool.Config()
	config.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	err = postgres.NewUnitOfWork(traced).Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		_, err := tx.StoreCards().CacheVersion(ctx)
		return err
	})
	if err != nil || trace.sql == "" {
		t.Fatal("не перехвачено адресное чтение поколения списка", err)
	}
	var raw []byte
	if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+trace.sql).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "store_card_cache_versions") {
		t.Fatalf("публичное чтение обходит поколения магазинов: %s", raw)
	}
	t.Logf("1000 поколений магазинов; EXPLAIN общего поколения: %s", raw)
}

// Проверяем стоимостной выбор на заполненных таблицах, не выключая последовательное чтение.
func TestMarketplaceReadIndexPlans(t *testing.T) {
	pool := testPool(t)
	seedMarketplaceReadPlans(t, pool)
	trace := &readPlanTrace{}
	config := pool.Config()
	config.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	uow := postgres.NewUnitOfWork(traced)
	for _, probe := range []struct {
		name    string
		match   string
		indexes []string
		rows    int
		run     func(context.Context, ports.Tx) error
	}{
		{
			name:    "moderation_history",
			match:   "FROM moderation_decisions d JOIN moderation_cases",
			indexes: []string{"moderation_cases_offer_history", "moderation_decisions_case_history"},
			rows:    2,
			run: func(ctx context.Context, tx ports.Tx) error {
				_, err := tx.ModerationDecisions().List(ctx, "t001000", "p000100")
				return err
			},
		},
		{
			name:    "promotion_history",
			match:   "FROM promotion_decisions WHERE",
			indexes: []string{"promotion_decisions_campaign_history"},
			rows:    2,
			run: func(ctx context.Context, tx ports.Tx) error {
				_, err := tx.Promotion().Decisions(ctx, "14ee22eaba297944c96afdbe5b16c65b")
				return err
			},
		},
		{
			name:    "storefront_category",
			match:   "FROM promotion_campaigns WHERE",
			indexes: []string{"promotion_campaigns_storefront"},
			rows:    11,
			run: func(ctx context.Context, tx ports.Tx) error {
				_, err := tx.Promotion().Select(ctx, promotion.Selection{
					Filter: promotion.Filter{
						Status:        promotion.Active,
						PlacementCode: "category_slot",
						From:          "2026-11-17",
						To:            "2026-11-17",
					},
					CategoryID: "c90",
				})
				return err
			},
		},
		{
			name:    "tick_page",
			match:   "FROM promotion_campaigns WHERE",
			indexes: []string{"promotion_campaigns_tick"},
			rows:    200,
			run: func(ctx context.Context, tx ports.Tx) error {
				_, err := tx.Promotion().Select(ctx, promotion.Selection{
					Filter: promotion.Filter{Status: promotion.Active},
					ByID:   true,
					Limit:  200,
					After:  "80000000-0000-0000-0000-000000000000",
				})
				return err
			},
		},
		{
			name:    "storefront_search",
			match:   "FROM promotion_campaigns WHERE",
			indexes: []string{"promotion_campaigns_offer_read"},
			rows:    1,
			run: func(ctx context.Context, tx ports.Tx) error {
				_, err := tx.Promotion().Select(ctx, promotion.Selection{
					Filter: promotion.Filter{
						Status:        promotion.Active,
						PlacementCode: "search_slot",
						From:          "2026-11-17",
						To:            "2026-11-17",
					},
					Offers: []ids.OfferID{{Tenant: "t001000", Product: "p000001"}},
				})
				return err
			},
		},
		{
			name:    "store_summary",
			match:   "DECLARE store_summary",
			indexes: []string{"catalog_products_store_summary"},
			rows:    2100,
			run: func(ctx context.Context, tx ports.Tx) error {
				return tx.Offers().WalkStoreSummaryOffers(ctx, "t001000", func([]ports.OfferState) error {
					return nil
				})
			},
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			trace.match, trace.sql, trace.args = probe.match, "", nil
			if err := uow.Do(context.Background(), probe.run); err != nil {
				t.Fatal(err)
			}
			if trace.sql == "" {
				t.Fatal("не перехвачен запрос репозитория")
			}
			sql := strings.TrimPrefix(trace.sql, "DECLARE store_summary NO SCROLL CURSOR FOR ")
			checkMarketplaceReadPlan(t, pool, sql, trace.args, probe.indexes, probe.rows)
		})
	}
}

func seedMarketplaceReadPlans(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{sql: offerPlanSeedTenants, args: []any{1000, at(0)}},
		{sql: offerPlanSeedParticipants, args: []any{at(0)}},
		{sql: readPlanSeedProducts, args: []any{at(0)}},
		{sql: readPlanSeedCases, args: []any{at(0)}},
		{sql: readPlanSeedModerationDecisions, args: []any{at(0)}},
		{sql: readPlanSeedCampaigns, args: []any{at(0)}},
		{sql: readPlanSeedPromotionDecisions, args: []any{at(0)}},
		{sql: readPlanAnalyze},
	} {
		if _, err := pool.Exec(context.Background(), seed.sql, seed.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("1000 магазинов; 102000 товаров (2100 у целевого), по 100000 дел и кампаний, по 200000 решений")
}

func checkMarketplaceReadPlan(
	t *testing.T,
	pool *pgxpool.Pool,
	sql string,
	args []any,
	indexes []string,
	rows int,
) {
	t.Helper()
	var raw []byte
	err := pool.QueryRow(context.Background(), "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+sql, args...).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	var results []readPlanExplain
	if err := json.Unmarshal(raw, &results); err != nil || len(results) != 1 {
		t.Fatalf("план: %s; ошибка: %v", raw, err)
	}
	result := results[0]
	if result.Plan.ActualRows != rows {
		t.Fatalf("ожидалось %d строк: %s", rows, raw)
	}
	used := map[string]bool{}
	collectReadPlanIndexes(result.Plan, used)
	t.Logf(
		"rows=%d buffers=%d+%d execution=%.3fms indexes=%v",
		result.Plan.ActualRows,
		result.Plan.SharedHit,
		result.Plan.SharedRead,
		result.ExecutionTime,
		used,
	)
	for _, index := range indexes {
		if !used[index] {
			t.Errorf("планировщик не использует %s: %s", index, raw)
		}
	}
}

func collectReadPlanIndexes(node readPlanNode, used map[string]bool) {
	if node.IndexName != "" {
		used[node.IndexName] = true
	}
	for _, child := range node.Plans {
		collectReadPlanIndexes(child, used)
	}
}
