package catalog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestStockSyncUpdatesIndexWithoutStoreCardTask(t *testing.T) {
	ctx := context.Background()
	store, index, clock := fake.NewStore(), fake.NewIndex(), fake.NewClock(t1)
	seedSeller(store, "t1", participant.TenantActive, true)
	seedProduct(store, "t1", "p1")
	projector := appcatalog.NewProjector(store, clock, nil)
	stock := appcatalog.StockEvent{
		TenantID:  "t1",
		ProductID: "p1",
		Stock:     catalog.StockSignal{Available: 5, At: t1},
		Price:     &catalog.PriceSignal{Price: money.FromKopecks(12000), At: t1},
	}
	if err := projector.ApplyStock(ctx, stock); err != nil {
		t.Fatal(err)
	}
	rows := store.OutboxRows()
	if len(rows) != 1 || rows[0].Kind != appcatalog.TaskSyncOffer {
		t.Fatalf("изменение остатков должно поставить только индексацию: %+v", rows)
	}
	idx := indexer(store, index, clock)
	task := taskOf(appcatalog.SyncOfferTask("t1", "p1", t1))
	if err := idx.HandleTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if result := idx.HandleOfferBatch(ctx, []ports.Task{task}); len(result) != 1 || result[0] != nil {
		t.Fatal(result)
	}
	doc, ok := index.Doc("t1~p1")
	if !ok || doc.Available != 5 || doc.Price != 10000 || len(index.PutBatches) != 2 {
		t.Fatalf("индекс должен принять остаток и сохранить цену витрины: %+v", doc)
	}
	if rows := store.OutboxRows(); len(rows) != 1 || rows[0].Kind != appcatalog.TaskSyncOffer {
		t.Fatalf("индексация поставила пересчёт: %+v", rows)
	}
}

func TestUnpublishedSnapshotDoesNotQueueStoreCardTask(t *testing.T) {
	ctx := context.Background()
	store, clock := fake.NewStore(), fake.NewClock(t1)
	seedSeller(store, "t1", participant.TenantActive, true)
	projector := appcatalog.NewProjector(store, clock, nil)
	p := snapshot("t1", "p1", 1, t0)
	p.Card.Marketplace = nil
	p.Card.Publication.Published = false
	if err := projector.ApplyProduct(ctx, productEvent("e1", p)); err != nil {
		t.Fatal(err)
	}
	p.Revision++
	p.Price = money.FromKopecks(20000)
	if err := projector.ApplyProduct(ctx, productEvent("e2", p)); err != nil {
		t.Fatal(err)
	}
	work, err := store.Claim(ctx, ports.ClaimRequest{
		Kinds:      []string{appcatalog.TaskSyncOffer},
		Now:        clock.Now(),
		LeaseUntil: clock.Now().Add(time.Minute),
		Limit:      1,
		PerTenant:  1,
	})
	if err != nil || len(work.Tasks) != 1 {
		t.Fatal(work, err)
	}
	index := indexer(store, fake.NewIndex(), clock)
	if err := index.HandleTask(ctx, work.Tasks[0]); err != nil {
		t.Fatal(err)
	}
	for _, task := range store.OutboxRows() {
		if task.Kind == ports.TaskStoreCard {
			t.Fatal("товар вне витрины не влияет на сводку")
		}
	}
}

func TestStoreCardMarkSurvivesCoalescingAndIsTransferredOnce(t *testing.T) {
	ctx := context.Background()
	store, index, clock := fake.NewStore(), fake.NewIndex(), fake.NewClock(t1)
	seedSeller(store, "t1", participant.TenantActive, true)
	seedProduct(store, "t1", "p1")
	projector := appcatalog.NewProjector(store, clock, nil)
	stock := appcatalog.StockEvent{
		TenantID:  "t1",
		ProductID: "p1",
		Stock:     catalog.StockSignal{Available: 5, At: t1},
	}
	if err := projector.ApplyStock(ctx, stock); err != nil {
		t.Fatal(err)
	}
	change := snapshot("t1", "p1", 2, t1)
	change.Price = money.FromKopecks(20000)
	if err := projector.ApplyProduct(ctx, productEvent("e1", change)); err != nil {
		t.Fatal(err)
	}
	stock.Stock.At = t2
	if err := projector.ApplyStock(ctx, stock); err != nil {
		t.Fatal(err)
	}

	claim := func(kind string, want int) []ports.Task {
		t.Helper()
		result, err := store.Claim(ctx, ports.ClaimRequest{
			Kinds:      []string{kind},
			Now:        clock.Now(),
			Limit:      10,
			PerTenant:  10,
			LeaseUntil: clock.Now().Add(time.Minute),
		})
		if err != nil || len(result.Tasks) != want {
			t.Fatalf("%s: задач %d, нужно %d, ошибка %v", kind, len(result.Tasks), want, err)
		}
		return result.Tasks
	}
	idx := indexer(store, index, clock)
	offers := claim(appcatalog.TaskSyncOffer, 1)
	index.FailPut = errors.New("поиск недоступен")
	if results := idx.HandleOfferBatch(ctx, offers); len(results) != 1 || !errors.Is(results[0], index.FailPut) {
		t.Fatal(results)
	}
	clock.Advance(time.Minute)
	summary := claim(ports.TaskStoreCard, 1)[0]
	if err := store.Complete(ctx, summary.ID, summary.Attempts, clock.Now()); err != nil {
		t.Fatal(err)
	}
	retry := claim(appcatalog.TaskSyncOffer, 1)
	index.FailPut = nil
	if err := idx.HandleTask(ctx, retry[0]); err != nil {
		t.Fatal(err)
	}
	claim(ports.TaskStoreCard, 0)
}
