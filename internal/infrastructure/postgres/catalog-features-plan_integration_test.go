//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// На маленькой выборке полный проход дешевле; сортировку оставляем, чтобы проверить отбор по категории.
	categoryRecommendationPlanSettings = `SET LOCAL enable_seqscan = off`
	// Для popular запрещаем и сортировку, чтобы проверить чтение верха по упорядоченному индексу.
	popularRecommendationPlanSettings = `SET LOCAL enable_seqscan = off; SET LOCAL enable_sort = off`
)

type recommendationQueryTrace struct {
	sql  string
	args []any
}

func (trace *recommendationQueryTrace) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, "WITH top_candidates") {
		trace.sql, trace.args = data.SQL, data.Args
	}
	return ctx
}

func (*recommendationQueryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestRecommendationPartialIndexPlans(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedRecommendationTenant(t, uow, "t1")
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
		settings   string
	}{
		{
			name:       "catalog_recommendations_category",
			categories: []string{"c1"},
			settings:   categoryRecommendationPlanSettings,
		},
		{name: "catalog_recommendations_popular", settings: popularRecommendationPlanSettings},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := repository.Recommend(ctx, ports.RecommendationFilter{
				Categories: tc.categories,
				Excluded:   []ids.OfferID{{Tenant: "t9", Product: "p9"}},
				Limit:      1,
			})
			if err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, tc.settings); err != nil {
				t.Fatal(err)
			}
			rows, err := tx.Query(ctx, "EXPLAIN "+trace.sql, trace.args...)
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
			if !strings.Contains(plan.String(), "using "+tc.name+" on catalog_products p_1") {
				t.Fatalf("не используется частичный индекс %s:\n%s", tc.name, plan.String())
			}
			if len(tc.categories) > 0 && !strings.Contains(plan.String(), "Index Cond: ((category_id = ANY") {
				t.Fatalf("нет индексного условия категории:\n%s", plan.String())
			}
		})
	}
}
