//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func TestPostgresStoreCardTriggerFollowsSnapshotNotStock(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedTenant(t, uow, "t1", "t1", participant.TenantActive)
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		p, err := participant.New(tenant, "Магазин", at(0))
		if err != nil {
			return err
		}
		return tx.Participants().Add(ctx, p)
	}); err != nil {
		t.Fatal(err)
	}

	queue := postgres.NewTaskQueue(postgres.Single(pool))
	clock := fake.NewClock(at(1))
	projector := appcatalog.NewProjector(uow, clock, nil)
	event := appcatalog.StockEvent{
		TenantID:  "t1",
		ProductID: "p1",
		Stock:     catalog.StockSignal{Available: 7, At: at(1)},
	}
	if err := projector.ApplyStock(ctx, event); err != nil {
		t.Fatal(err)
	}
	snap := snapshot("t1", "p1", 1, 0)
	snap.Card.Marketplace = &taxonomy.Card{CategoryID: "c1", Published: true}
	if err := projector.ApplyProduct(ctx, appcatalog.ProductEvent{Snapshot: snap}); err != nil {
		t.Fatal(err)
	}
	event.Stock.At = at(2)
	if err := projector.ApplyStock(ctx, event); err != nil {
		t.Fatal(err)
	}

	request := ports.ClaimRequest{Now: at(1), LeaseUntil: at(2), Limit: 10, PerTenant: 10}
	claim := func(kind string, want int) []ports.Task {
		t.Helper()
		request.Kinds = []string{kind}
		result, err := queue.Claim(ctx, request)
		if err != nil || len(result.Tasks) != want {
			t.Fatalf("%s: задач %d, нужно %d, ошибка %v", kind, len(result.Tasks), want, err)
		}
		return result.Tasks
	}
	index := fake.NewIndex()
	indexer := appcatalog.NewIndexer(uow, index, clock, 200, nil, nil, time.Minute)
	offers := claim(appcatalog.TaskSyncOffer, 1)
	if result := indexer.HandleOfferBatch(ctx, offers); len(result) != 1 || result[0] != nil {
		t.Fatal(result)
	}
	request.Now, request.LeaseUntil = at(2), at(3)
	first := claim(ports.TaskStoreCard, 1)[0]
	if err := queue.Complete(ctx, first.ID, first.Attempts, clock.Now()); err != nil {
		t.Fatal(err)
	}
	// Незавершённая индексация повторится после аренды, дельты уже запланированы.
	offers = claim(appcatalog.TaskSyncOffer, 1)
	if result := indexer.HandleOfferBatch(ctx, offers); len(result) != 1 || result[0] != nil {
		t.Fatal(result)
	}
	claim(ports.TaskStoreCard, 0)
	if len(index.PutBatches) != 2 {
		t.Fatalf("вызовов индекса: %d", len(index.PutBatches))
	}
}
