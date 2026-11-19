//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	countRevalidationFailures = `SELECT count(*) FROM taxonomy_revalidation_failures`
	resolveRetainedFailures   = `UPDATE catalog_products
        SET version = 2,
            revision = CASE WHEN product_id = 'p002' THEN 2 ELSE revision END,
            card = jsonb_set(card, '{publication,version}', '2')
        WHERE product_id IN ('p001', 'p002')`
	deleteFailedProduct        = `DELETE FROM catalog_products WHERE product_id = 'p003'`
	updateFailedModelKey       = `UPDATE catalog_products SET version = 2 WHERE product_id = 'p004'`
	keepExplicitCurrentFailure = `UPDATE catalog_products
        SET card = jsonb_set(card, '{publication,version}', '2') WHERE product_id = 'p005'`
)

func TestTaxonomyFailureQueryAndRetentionSQL(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)
	config := pool.Config()
	trace := &revalidationSQLTrace{}
	config.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	uow := postgres.NewUnitOfWork(traced)
	service, _ := prepareModeratedRevalidation(t, uow, 6)
	seedRetainedFailures(t, uow)
	for _, query := range []string{
		resolveRetainedFailures,
		deleteFailedProduct,
		updateFailedModelKey,
		keepExplicitCurrentFailure,
	} {
		if _, err := pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	trace.reset()
	items, err := service.RevalidationFailures(ctx, ports.OfferKey{}, 1)
	if err != nil || len(items) != 1 || items[0].ProductID != "p000" || len(trace.snapshot()) != 1 {
		t.Fatal("первая страница должна читать один SQL", items, err, trace.snapshot())
	}
	if utf8.RuneCountInString(items[0].Diagnostic) != 2048 {
		t.Fatal("диагностика не ограничена", len(items[0].Diagnostic))
	}
	items, err = service.RevalidationFailures(ctx, ports.OfferKey{TenantID: "t1", ProductID: "p000"}, 100)
	if err != nil || len(items) != 2 || items[0].ProductID != "p004" || items[1].ProductID != "p005" {
		t.Fatal("неактуальная ошибка попала в ответ", items, err)
	}
	trace.reset()
	result, err := postgres.NewRetention(traced).Purge(ctx, ports.PurgeRequest{Batch: 2})
	if err != nil || result.RevalidationFailures != 3 || len(trace.snapshot()) != 11 {
		t.Fatal("7 штатных SQL (включая bulk retention) и 4 страницы отказов", result, err, trace.snapshot())
	}
	var remaining int
	if err := pool.QueryRow(ctx, countRevalidationFailures).Scan(&remaining); err != nil || remaining != 3 {
		t.Fatal("retention удалила текущие ошибки или оставила исправленные", remaining, err)
	}
}

func seedRetainedFailures(t *testing.T, uow ports.UnitOfWork) {
	t.Helper()
	items := []ports.RevalidationFailure{}
	for i := 0; i < 6; i++ {
		items = append(items, ports.RevalidationFailure{
			TenantID:        "t1",
			ProductID:       ids.ProductID(fmt.Sprintf("p%03d", i)),
			ProductVersion:  1,
			SourceRevision:  1,
			TaxonomyVersion: 2,
			Diagnostic:      strings.Repeat("я", 5000),
			At:              time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		})
	}
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Revalidation().RecordFailures(ctx, items)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTaxonomySuccessfulRetryRetiresFailure(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	service, definition := prepareModeratedRevalidation(t, uow, 1)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Revalidation().RecordFailures(ctx, []ports.RevalidationFailure{
			{
				TenantID:        "t1",
				ProductID:       "p000",
				ProductVersion:  1,
				SourceRevision:  1,
				TaxonomyVersion: 2,
				Diagnostic:      "save: исправленная ошибка данных",
				At:              time.Now(),
			},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	definition.Attributes[0].Name = "Изменённое поле"
	if _, err := service.LoadPart(ctx, "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	drainTaxonomyPages(t, service)
	items, err := service.RevalidationFailures(ctx, ports.OfferKey{}, 100)
	if err != nil || len(items) != 0 {
		t.Fatal("успешная проверка не убрала ошибку из ответа", items, err)
	}
	var count int
	if err := pool.QueryRow(ctx, countRevalidationFailures).Scan(&count); err != nil || count != 1 {
		t.Fatal("физическое удаление должно выполняться retention", count, err)
	}
	result, err := postgres.NewRetention(pool).Purge(ctx, ports.PurgeRequest{Batch: 1000})
	if err != nil || result.RevalidationFailures != 1 {
		t.Fatal("retention не удалила исправленную запись", result, err)
	}
}
