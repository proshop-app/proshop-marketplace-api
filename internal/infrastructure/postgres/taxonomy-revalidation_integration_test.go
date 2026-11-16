//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	failRevalidationProgress  = `ALTER TABLE taxonomy_revalidation ADD CONSTRAINT fail_page CHECK (processed <= 50)`
	allowRevalidationProgress = `ALTER TABLE taxonomy_revalidation DROP CONSTRAINT fail_page`
	readRevalidationProgress  = `SELECT processed FROM taxonomy_revalidation WHERE version = 2 AND category = 'c1'`
	countRevalidationOutbox   = `SELECT count(*) FROM outbox`
	categoryPagePlan          = `EXPLAIN (COSTS OFF) SELECT product_id FROM catalog_products
		WHERE card->'marketplace'->>'categoryId' = 'c1'
		AND (tenant_id, product_id) > ('t1', 'p049') ORDER BY tenant_id, product_id LIMIT 50 FOR UPDATE`
)

func TestTaxonomyPageRollbackAndResume(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	service := apptaxonomy.NewService(uow, clock.System{}, nil)
	definition := testTaxonomy(1)
	if err := applyDefinition(ctx, service, definition); err != nil {
		t.Fatal(err)
	}
	drainTaxonomyPages(t, service)
	seedTaxonomyProducts(t, uow, definition, 55)
	definition.Attributes[0].Name = "Изменённое поле"
	if _, err := service.LoadPart(ctx, "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	job, err := service.RevalidateNext(ctx)
	if err != nil || job == nil || job.Processed != 50 || job.Done {
		t.Fatal(job, err)
	}
	if _, err := pool.Exec(ctx, failRevalidationProgress); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RevalidateNext(ctx); err == nil {
		t.Fatal("ошибка сохранения прогресса не вернулась")
	}
	var processed, tasks int
	if err := pool.QueryRow(ctx, readRevalidationProgress).Scan(&processed); err != nil || processed != 50 {
		t.Fatal("курсор продвинулся при сбое", processed, err)
	}
	if err := pool.QueryRow(ctx, countRevalidationOutbox).Scan(&tasks); err != nil || tasks != 100 {
		t.Fatal("сбой оставил задачи", tasks, err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		product, err := tx.Products().Get(ctx, "t1", "p054")
		if err == nil && product.State().Card.Publication.Version != 1 {
			t.Fatal("сбой сохранил товар")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, allowRevalidationProgress); err != nil {
		t.Fatal(err)
	}
	service = apptaxonomy.NewService(uow, clock.System{}, nil)
	job, err = service.RevalidateNext(ctx)
	if err != nil || job == nil || job.Processed != 55 || !job.Done || job.ProductCursor != "p054" {
		t.Fatal("перезапуск потерял прогресс", job, err)
	}
	if err := pool.QueryRow(ctx, countRevalidationOutbox).Scan(&tasks); err != nil || tasks != 110 {
		t.Fatal("страница повторила завершённые товары", tasks, err)
	}
	assertCategoryPageIndex(t, pool)
}

func TestTaxonomyPendingPagesAllowReadsAndEvents(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	service := apptaxonomy.NewService(uow, clock.System{}, nil)
	definition := testTaxonomy(1)
	if err := applyDefinition(ctx, service, definition); err != nil {
		t.Fatal(err)
	}
	drainTaxonomyPages(t, service)
	seedTaxonomyProducts(t, uow, definition, 55)
	definition.Attributes[0].Name = "Изменённое поле"
	if _, err := service.LoadPart(ctx, "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		job, err := tx.Taxonomy().NextRevalidation(ctx)
		if err != nil {
			return err
		}
		if job == nil {
			return errors.New("нет задания")
		}
		if _, err := tx.Taxonomy().Current(ctx); err != nil {
			return err
		}
		// Активный worker держит голову FOR SHARE и строки своей страницы.
		if _, err := tx.Products().RevalidationPage(ctx, *job, 50); err != nil {
			return err
		}
		other, err := service.RevalidateNext(ctx)
		if err != nil {
			return err
		}
		if other != nil {
			return errors.New("вторая реплика взяла уже занятую категорию")
		}
		queries := appcatalog.NewQueries(uow, nil, nil, nil, nil, clock.System{})
		if _, err := queries.PrepareQuery(ctx, ports.OfferQuery{}); err != nil {
			return err
		}
		projector := appcatalog.NewProjector(uow, clock.System{}, nil)
		return projector.ApplyProduct(ctx, appcatalog.ProductEvent{
			EventID: "between-pages",
			Snapshot: catalog.Snapshot{
				TenantID:  "t1",
				ProductID: "p054",
				Revision:  2,
				At:        time.Now(),
				Card: catalog.Card{
					Name:        "Товар",
					Visible:     true,
					Marketplace: &taxonomy.Card{CategoryID: "c1", Published: true},
				},
			},
		})
	})
	if err != nil {
		t.Fatal("worker блокирует чтение или событие вне страницы", err)
	}
	drainTaxonomyPages(t, service)
}

func seedTaxonomyProducts(t *testing.T, uow ports.UnitOfWork, definition taxonomy.Definition, count int) {
	t.Helper()
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		for i := 0; i < count; i++ {
			snapshot := catalog.Snapshot{
				TenantID:  "t1",
				ProductID: ids.ProductID(fmt.Sprintf("p%03d", i)),
				Revision:  1,
				At:        time.Now(),
				Card: catalog.Card{
					Name:        "Товар",
					Visible:     true,
					Marketplace: &taxonomy.Card{CategoryID: "c1", Published: true},
				},
			}
			product, err := catalog.NewProduct(snapshot, time.Now())
			if err != nil {
				return err
			}
			product.Revalidate(definition, time.Now())
			if err := tx.Products().Save(ctx, product); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func drainTaxonomyPages(t *testing.T, service *apptaxonomy.Service) {
	t.Helper()
	for {
		job, err := service.RevalidateNext(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if job == nil {
			return
		}
	}
}

func assertCategoryPageIndex(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Маленькая фикстура может предпочесть seq scan; проверяем пригодность индекса для keyset-страницы.
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, categoryPagePlan)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	lines := []string{}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(lines, "\n")
	if !strings.Contains(plan, "catalog_products_taxonomy_category_idx") ||
		!strings.Contains(plan, "Index Cond:") {
		t.Fatal("страница не использует индекс категории", plan)
	}
}
