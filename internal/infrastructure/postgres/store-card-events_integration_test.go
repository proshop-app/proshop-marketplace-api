//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	readCacheMetadata = `SELECT (SELECT version FROM store_card_cache_generation WHERE id = 1),
 (SELECT version FROM store_card_cache_versions WHERE tenant_id = 't1'), (SELECT count(*) FROM store_cards)`
	lockCacheMetadata = `LOCK TABLE store_cards, store_card_cache_versions, store_card_cache_generation
 IN ACCESS EXCLUSIVE MODE`
)

// Даже чужая незавершённая запись метаданных карточек не должна задержать события товаров.
func TestProductEventsDoNotTouchStoreCardGenerations(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	prepareModeratedRevalidation(t, postgres.NewUnitOfWork(pool), 0)
	if _, err := pool.Exec(ctx, cacheGenerationSeed); err != nil {
		t.Fatal(err)
	}
	before := cacheMetadata(t, pool)
	trace := &revalidationSQLTrace{}
	config := pool.Config()
	config.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	uow := postgres.NewUnitOfWork(traced)
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if _, err := tx.Taxonomy().Current(ctx); err != nil {
			return err
		}
		_, err := tx.ModerationRules().Current(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	trace.reset()
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback(ctx) }()
	if _, err := lock.Exec(ctx, lockCacheMetadata); err != nil {
		t.Fatal(err)
	}
	moderator := appmoderation.NewService(uow, clock.System{}, random.IDs{}, appmoderation.Config{})
	projector := appcatalog.NewProjector(uow, clock.System{}, moderator)
	budget, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	workers, work := errgroup.WithContext(budget)
	for n := 0; n < 8; n++ {
		workers.Go(func() error { return projector.ApplyProduct(work, cacheProductEvent(n, 1)) })
	}
	if err := workers.Wait(); err != nil {
		t.Fatal("приём товаров ждёт метаданные фоновой карточки", err)
	}
	t.Logf("ApplyProduct: %d SQL на 8 новых модерируемых товаров; BEGIN/COMMIT исключены", len(trace.snapshot()))
	trace.reset()
	// После Wait контекст errgroup отменён; обновление и удаление проверяем с исходным бюджетом.
	update := cacheProductEvent(0, 2)
	if err := projector.ApplyProduct(budget, update); err != nil {
		t.Fatal(err)
	}
	t.Logf("ApplyProduct неизменное содержимое с новой ревизией: %d SQL", len(trace.snapshot()))
	deleted := cacheProductEvent(0, 3)
	deleted.Snapshot.Deleted = true
	if err := projector.ApplyProduct(budget, deleted); err != nil {
		t.Fatal(err)
	}
	if err := lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if after := cacheMetadata(t, pool); after != before {
		t.Fatalf("событие изменило готовую карточку/поколение: %v → %v", before, after)
	}
}

func cacheMetadata(t *testing.T, pool *pgxpool.Pool) [3]int64 {
	t.Helper()
	var result [3]int64
	err := pool.QueryRow(context.Background(), readCacheMetadata).Scan(&result[0], &result[1], &result[2])
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func cacheProductEvent(n int, revision int64) appcatalog.ProductEvent {
	return appcatalog.ProductEvent{
		Snapshot: catalog.Snapshot{
			TenantID:  "t1",
			ProductID: ids.ProductID(fmt.Sprintf("p%03d", n)),
			Revision:  revision,
			At:        time.Now(),
			Card: catalog.Card{
				Name:        "Товар",
				Visible:     true,
				Marketplace: &taxonomy.Card{CategoryID: "c1", Published: true},
			},
		},
	}
}
