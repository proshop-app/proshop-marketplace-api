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
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// На малой фикстуре проверяем доступность индексного пути, а не стоимостной выбор планировщика.
	recommendationPlanSettings = `SET LOCAL enable_seqscan = off`
	recommendationPlanLimit    = 5
)

type recommendationPlanNode struct {
	NodeType     string                   `json:"Node Type"`
	RelationName string                   `json:"Relation Name"`
	IndexName    string                   `json:"Index Name"`
	IndexCond    string                   `json:"Index Cond"`
	SubplanName  string                   `json:"Subplan Name"`
	ActualRows   float64                  `json:"Actual Rows"`
	Plans        []recommendationPlanNode `json:"Plans"`
}

type recommendationExplain struct {
	Plan recommendationPlanNode `json:"Plan"`
}

type recommendationQueryTrace struct {
	sql  string
	args []any
}

func (trace *recommendationQueryTrace) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, "WITH eligible") {
		trace.sql, trace.args = data.SQL, data.Args
	}
	return ctx
}

func (*recommendationQueryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestRecommendationPartialIndexPlans(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedRecommendationProducts(t, uow, "t1", 240)
	seedRecommendationProducts(t, uow, "t2", 80)
	ctx := context.Background()
	trace := &recommendationQueryTrace{}
	config := pool.Config()
	config.ConnConfig.Tracer = trace
	tracedPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer tracedPool.Close()
	repository := postgres.NewCatalogFeatures(tracedPool)
	for _, tc := range []struct {
		name       string
		categories []string
	}{
		{
			name:       "personal",
			categories: []string{"c1"},
		},
		{name: "popular"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := repository.Recommend(ctx, ports.RecommendationFilter{
				Categories: tc.categories,
				Excluded:   []ids.OfferID{{Tenant: "t9", Product: "p9"}},
				Limit:      recommendationPlanLimit,
			})
			if err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, recommendationPlanSettings); err != nil {
				t.Fatal(err)
			}
			if trace.sql == "" {
				t.Fatal("не перехвачен запрос рекомендаций")
			}
			var raw []byte
			err = tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+trace.sql, trace.args...).Scan(&raw)
			if err != nil {
				t.Fatal(err)
			}
			var explain []recommendationExplain
			if err := json.Unmarshal(raw, &explain); err != nil {
				t.Fatal(err)
			}
			if len(explain) != 1 {
				t.Fatalf("неожиданный план: %s", raw)
			}
			if !checkRecommendationPlan(t, explain[0].Plan, false) {
				t.Fatalf("нет упорядоченного индексного прохода по магазину под Limit: %s", raw)
			}
		})
	}
}

func checkRecommendationPlan(t *testing.T, node recommendationPlanNode, limited bool) bool {
	t.Helper()
	if node.RelationName == "catalog_products" && strings.Contains(node.NodeType, "Seq Scan") {
		t.Fatalf("последовательный проход по товарам: %+v", node)
	}
	if node.SubplanName == "CTE eligible" && node.ActualRows > 2*recommendationPlanLimit {
		t.Fatalf("набор кандидатов не ограничен числом магазинов и limit: %+v", node)
	}
	if node.NodeType == "Limit" {
		limited = node.ActualRows <= recommendationPlanLimit
	}
	// Сортировка под Limit потребовала бы прочитать весь набор магазина до отсечения.
	if strings.Contains(node.NodeType, "Sort") {
		limited = false
	}
	found := node.IndexName == "catalog_recommendations_tenant_rank"
	tenantCondition := strings.Contains(node.IndexCond, "tenant_id = m.tenant_id") ||
		strings.Contains(node.IndexCond, "tenant_id = t.tenant_id")
	if found && (!limited || !tenantCondition) {
		t.Fatalf("индекс не ограничен магазином или лимитом: %+v", node)
	}
	for _, child := range node.Plans {
		found = checkRecommendationPlan(t, child, limited) || found
	}
	return found
}
