//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	countBatchVerdicts      = `SELECT count(*) FROM moderation_verdicts WHERE taxonomy_version = 2`
	countBatchCases         = `SELECT count(*) FROM moderation_cases WHERE status = 'open'`
	countBatchHistory       = `SELECT count(*) FROM moderation_verdict_log WHERE taxonomy_version = 2`
	corruptRevalidationCard = `UPDATE catalog_products
        SET card = jsonb_set(jsonb_set(card, '{assets}', '17'), '{publication,version}', '2')
        WHERE product_id = 'p001'`
	failOneRevalidationSave = `ALTER TABLE catalog_products ADD CONSTRAINT bad_revalidation_card
        CHECK (product_id <> 'p002' OR version < 2)`
	corruptRevalidationVerdict = `INSERT INTO moderation_verdicts
        (tenant_id, product_id, revision, rules_revision, taxonomy_version, status, reasons,
            decided_by, manual_reject, version, updated_at)
        VALUES ('t1', 'p003', 1, 1, 1, 'pending', '{}', 'rule', false, 1, now())`
	corruptRevalidationPublicationCategory = `UPDATE catalog_products
        SET card = jsonb_set(card, '{publication,categoryId}', '"c2"') WHERE product_id = 'p004'`
	corruptRevalidationTimestamp = `UPDATE catalog_products SET updated_at = 'infinity' WHERE product_id = 'p005'`
)

type revalidationSQLTrace struct {
	mu      sync.Mutex
	queries []string
}

type rollbackTaxonomyPageUOW struct {
	ports.UnitOfWork
	calls int
}

func (u *rollbackTaxonomyPageUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	u.calls++
	return u.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		if u.calls == 2 {
			return context.DeadlineExceeded
		}
		return nil
	})
}

func (t *revalidationSQLTrace) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	query := strings.TrimSpace(strings.ToLower(data.SQL))
	if query != "begin" && query != "commit" && query != "rollback" {
		t.mu.Lock()
		t.queries = append(t.queries, data.SQL)
		t.mu.Unlock()
	}
	return ctx
}

func (t *revalidationSQLTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (t *revalidationSQLTrace) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queries = nil
}

func (t *revalidationSQLTrace) snapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string{}, t.queries...)
}

func TestTaxonomyBatchSQLAndModeration(t *testing.T) {
	pool := pgtest.Pool(t)
	config := pool.Config()
	trace := &revalidationSQLTrace{}
	config.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	uow := postgres.NewUnitOfWork(traced)
	service, definition := prepareModeratedRevalidation(t, uow, 100)
	definition.Attributes[0].Name = "Изменённое поле"
	if _, err := service.LoadPart(context.Background(), "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		processed int64
		queries   int
	}{
		{processed: 50, queries: 16},  // Обе версии отсутствуют в снимках UoW.
		{processed: 100, queries: 14}, // Обе версии уже прочитаны предыдущей страницей.
	} {
		for {
			trace.reset()
			job, err := service.RevalidateNext(context.Background())
			if err != nil || job == nil {
				t.Fatal(job, err)
			}
			if job.Category != "c1" {
				continue
			}
			queries := trace.snapshot()
			if job.Processed != test.processed || len(queries) != test.queries {
				t.Fatalf(
					"страница %+v: %d SQL, ожидалось %d:\n%s",
					job,
					len(queries),
					test.queries,
					strings.Join(queries, "\n"),
				)
			}
			break
		}
	}
	for query, expected := range map[string]int{countBatchVerdicts: 100, countBatchCases: 100, countBatchHistory: 100} {
		var count int
		if err := pool.QueryRow(context.Background(), query).Scan(&count); err != nil || count != expected {
			t.Fatal(count, err)
		}
	}
	// Повторная версия обновляет существующие оценки/дела через тот же пакетный CAS.
	definition.Attributes[0].Name = "Следующее поле"
	if _, err := service.LoadPart(context.Background(), "attributes", definition, 2); err != nil {
		t.Fatal(err)
	}
	drainTaxonomyPages(t, service)
}

func TestTaxonomyPermanentRowsDoNotBlockCategories(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	service, definition := prepareModeratedRevalidation(t, uow, 55)
	seedExtraRevalidationProduct(t, uow, definition)
	for _, query := range []string{
		corruptRevalidationCard,
		failOneRevalidationSave,
		corruptRevalidationVerdict,
		corruptRevalidationPublicationCategory,
		corruptRevalidationTimestamp,
	} {
		if _, err := pool.Exec(context.Background(), query); err != nil {
			t.Fatal(err)
		}
	}
	definition.Attributes[0].Name = "Изменённое поле"
	if _, err := service.LoadPart(context.Background(), "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	drainTaxonomyPages(t, service)
	failures, err := service.RevalidationFailures(context.Background(), ports.OfferKey{}, 50)
	if err != nil || len(failures) != 4 {
		t.Fatalf("диагностика повреждённых карточек: %+v, %v", failures, err)
	}
	if failures[0].ProductID != "p001" || failures[1].ProductID != "p002" || failures[2].ProductID != "p003" ||
		!strings.HasPrefix(failures[0].Diagnostic, "decode:") || !strings.HasPrefix(failures[1].Diagnostic, "save:") ||
		!strings.HasPrefix(failures[2].Diagnostic, "moderation verdict:") ||
		failures[3].ProductID != "p005" || !strings.HasPrefix(failures[3].Diagnostic, "decode:") {
		t.Fatal(failures)
	}
	var count int
	if err := pool.QueryRow(context.Background(), countBatchVerdicts).Scan(&count); err != nil || count != 52 {
		t.Fatal("одна ошибка остановила здоровые карточки", count, err)
	}
	if err := pool.QueryRow(context.Background(), countBatchCases).Scan(&count); err != nil || count != 52 {
		t.Fatal("частично сохранена модерация плохой карточки", count, err)
	}
}

func TestTaxonomyTemporaryPageErrorAllowsOtherCategory(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	service, definition := prepareModeratedRevalidation(t, uow, 55)
	seedExtraRevalidationProduct(t, uow, definition)
	definition.Attributes[0].Name = "Изменённое поле"
	if _, err := service.LoadPart(ctx, "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	failing := &rollbackTaxonomyPageUOW{UnitOfWork: uow}
	worker := apptaxonomy.NewService(failing, clock.System{}, nil)
	if _, err := worker.RevalidateNext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("страница не вернула временную ошибку", err)
	}
	job, err := service.RevalidateNext(ctx)
	if err != nil || job == nil || job.Category != "c2" || !job.Done {
		t.Fatal("аренда не освободила очередь для другой категории", job, err)
	}
	time.Sleep(1100 * time.Millisecond)
	job, err = service.RevalidateNext(ctx)
	if err != nil || job == nil || job.Category != "c1" || job.Processed != 50 {
		t.Fatal("страница не повторилась с прежнего курсора", job, err)
	}
	drainTaxonomyPages(t, service)
}

func prepareModeratedRevalidation(
	t *testing.T,
	uow ports.UnitOfWork,
	count int,
) (*apptaxonomy.Service, taxonomy.Definition) {
	t.Helper()
	moderation := appmoderation.NewService(uow, clock.System{}, random.IDs{}, appmoderation.Config{})
	service := apptaxonomy.NewService(uow, clock.System{}, moderation)
	definition := testTaxonomy(1)
	definition.Categories = append(definition.Categories, taxonomy.Category{
		ID:     "c2",
		Slug:   "c2",
		Name:   "Другая категория",
		Schema: "s1",
	})
	schema := definition.Schemas["s1"]
	schema.Optional = []string{"a1"}
	definition.Schemas["s1"] = schema
	if err := applyDefinition(context.Background(), service, definition); err != nil {
		t.Fatal(err)
	}
	drainTaxonomyPages(t, service)
	seedRevalidationMember(t, uow, "t1")
	seedTaxonomyProducts(t, uow, definition, count)
	return service, definition
}

func seedRevalidationMember(t *testing.T, uow ports.UnitOfWork, id ids.TenantID) {
	t.Helper()
	now := time.Now()
	event := participant.RegistryEvent{
		TenantID: id,
		Slug:     "shop-" + string(id),
		Status:   participant.TenantActive,
		At:       now,
	}
	tenant, err := participant.NewTenant(event, now)
	if err != nil {
		t.Fatal(err)
	}
	member, err := participant.New(tenant, "Магазин", now)
	if err != nil {
		t.Fatal(err)
	}
	err = uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		if err := tx.Registry().Save(ctx, tenant); err != nil {
			return err
		}
		return tx.Participants().Add(ctx, member)
	})
	if err != nil {
		t.Fatal(fmt.Errorf("участник %s: %w", id, err))
	}
}

func seedExtraRevalidationProduct(t *testing.T, uow ports.UnitOfWork, definition taxonomy.Definition) {
	t.Helper()
	snapshot := catalog.Snapshot{
		TenantID:  "t1",
		ProductID: "q1",
		Revision:  1,
		At:        time.Now(),
		Card: catalog.Card{
			Name:        "Товар",
			Visible:     true,
			Marketplace: &taxonomy.Card{CategoryID: "c2", Published: true},
		},
	}
	product, err := catalog.NewProduct(snapshot, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	product.Revalidate(definition, time.Now())
	err = uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Products().Save(ctx, product)
	})
	if err != nil {
		t.Fatal(err)
	}
}
