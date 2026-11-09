//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const untouchedProductsSQL = `SELECT count(*) FROM catalog_products
 WHERE tenant_id = 't2' AND (card->'publication'->>'version')::bigint = 0`

const batchTasksSQL = `SELECT count(*) FILTER (WHERE tenant_id = 't1'),
 count(*) FILTER (WHERE tenant_id = 't2') FROM outbox WHERE kind = $1`

const continuationSQL = `SELECT id FROM outbox
 WHERE kind = $1 AND done_at IS NULL AND attempts = 0 AND next_at <= $2`

func TestTaxonomyOutboxCursorAndSupersession(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(pool)
	now := fake.NewClock(time.Now())
	evaluator := &fake.TaxonomyEvaluator{
		FailOffer: ids.OfferID{Tenant: "t2", Product: "p002"},
		Err:       errors.New("сбой пачки"),
	}
	service := apptaxonomy.NewService(uow, now, evaluator, 0)
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for i := 0; i < 205; i++ {
			tenant, id := ids.TenantID("t1"), ids.ProductID(fmt.Sprintf("p%03d", i))
			if i >= 200 {
				tenant, id = "t2", ids.ProductID(fmt.Sprintf("p%03d", i-200))
			}
			product, err := catalog.NewProduct(
				catalog.Snapshot{
					TenantID:  tenant,
					ProductID: id,
					Revision:  1,
					At:        now.Now(),
					Card:      catalog.Card{Name: "Товар"},
				},
				now.Now(),
			)
			if err != nil {
				return err
			}
			if err := tx.Products().Save(ctx, product); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := applyDefinition(ctx, service, testTaxonomy(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.LoadPart(ctx, "categories", testTaxonomy(1), 1); err != nil {
		t.Fatal(err)
	}
	queue := postgres.NewTaskQueue(pool)
	tasks, err := queue.Claim(ctx, ports.ClaimRequest{
		Now:        now.Now(),
		LeaseUntil: now.Now().Add(time.Minute),
		Limit:      1,
		PerTenant:  1,
	})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("задача: %+v, %v", tasks, err)
	}
	var payload struct {
		Version int64 `json:"version"`
	}
	if err := json.Unmarshal(tasks[0].Payload, &payload); err != nil || payload.Version != 2 {
		t.Fatalf("ожидающая задача не заменена: %s, %v", tasks[0].Payload, err)
	}
	if err := service.HandleTask(ctx, tasks[0]); err != nil {
		t.Fatal(err)
	}
	var continuationID int64
	err = pool.QueryRow(ctx, continuationSQL, apptaxonomy.TaskRevalidate, now.Now()).Scan(&continuationID)
	if err != nil {
		t.Fatal(err)
	}
	if continuationID == tasks[0].ID {
		t.Fatal("продолжение склеилось со взятой задачей")
	}
	continuation := claimRevalidation(t, queue, now.Now())
	if continuation.ID != continuationID {
		t.Fatal("взята другая строка продолжения")
	}
	if err := service.HandleTask(ctx, continuation); !errors.Is(err, evaluator.Err) {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.TaxonomyRevalidation().LockProgress(ctx)
		if err != nil {
			return err
		}
		if p.Version != 2 || p.TenantID != "t1" || p.ProductID != "p199" || p.Done {
			t.Fatalf("курсор после сбоя: %+v", p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var untouched int
	if err := pool.QueryRow(ctx, untouchedProductsSQL).Scan(&untouched); err != nil {
		t.Fatal(err)
	}
	if untouched != 5 {
		t.Fatalf("сбой сохранил товары: %d", untouched)
	}
	for _, kind := range []string{"catalog.offer.sync", "moderation.offer.status"} {
		var committed, rolledBack int
		if err := pool.QueryRow(ctx, batchTasksSQL, kind).
			Scan(&committed, &rolledBack); err != nil {
			t.Fatal(err)
		}
		if committed != 200 || rolledBack != 0 {
			t.Fatalf("атомарность %s: %d/%d", kind, committed, rolledBack)
		}
	}
	evaluator.Err = nil
	if err := service.HandleTask(ctx, continuation); err != nil {
		t.Fatal(err)
	}
	if err := queue.Complete(ctx, continuation.ID, now.Now()); err != nil {
		t.Fatal(err)
	}
	if len(evaluator.Calls) != 208 {
		t.Fatalf("повтор обработанных товаров: %d", len(evaluator.Calls))
	}
	if _, err := service.LoadPart(ctx, "categories", testTaxonomy(1), 2); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleTask(ctx, tasks[0]); err != nil {
		t.Fatal(err)
	}
	if err := queue.Complete(ctx, tasks[0].ID, now.Now()); err != nil {
		t.Fatal(err)
	}
	pending, err := queue.Claim(ctx, ports.ClaimRequest{
		Now:        now.Now(),
		LeaseUntil: now.Now().Add(time.Minute),
		Limit:      1000,
		PerTenant:  1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, task := range pending {
		if task.Kind == apptaxonomy.TaskRevalidate {
			count++
			if err := service.HandleTask(ctx, task); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Вторая пачка v3 тоже выполняется только через отдельную строку продолжения.
	next := claimRevalidation(t, queue, now.Now())
	if err := service.HandleTask(ctx, next); err != nil {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		progress, err := tx.TaxonomyRevalidation().LockProgress(ctx)
		if err == nil && (progress.Version != 3 || !progress.Done) {
			t.Fatalf("незавершённый проход v3: %+v", progress)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(evaluator.Calls) != 208 {
		t.Fatalf("замещение взятой задачи: %d, оценки: %d", count, len(evaluator.Calls))
	}
}

func claimRevalidation(t *testing.T, queue *postgres.TaskQueue, now time.Time) ports.Task {
	t.Helper()
	tasks, err := queue.Claim(context.Background(), ports.ClaimRequest{
		Now:        now,
		LeaseUntil: now.Add(time.Minute),
		Limit:      1000,
		PerTenant:  1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var found []ports.Task
	for _, task := range tasks {
		if task.Kind == apptaxonomy.TaskRevalidate {
			found = append(found, task)
		}
	}
	if len(found) != 1 {
		t.Fatalf("ожидалось одно продолжение: %+v", found)
	}
	return found[0]
}

func TestTaxonomyProgressPersistsDirtyCards(t *testing.T) {
	uow := postgres.NewUnitOfWork(pgtest.Pool(t))
	for _, dirty := range []bool{true, false} {
		want := ports.TaxonomyProgress{Version: 3, CardsVersion: 1, CardsDirty: dirty, TenantID: "t1", ProductID: "p1"}
		if err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
			if _, err := tx.TaxonomyRevalidation().LockProgress(ctx); err != nil {
				return err
			}
			return tx.TaxonomyRevalidation().SaveProgress(ctx, want)
		}); err != nil {
			t.Fatal(err)
		}
		if err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
			got, err := tx.TaxonomyRevalidation().LockProgress(ctx)
			if err == nil && got != want {
				t.Fatalf("прогресс: %+v, ожидался %+v", got, want)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}
