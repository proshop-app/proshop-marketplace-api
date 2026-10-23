package catalog_test

import (
	"context"
	"errors"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestApplyProduct(t *testing.T) {
	store := fake.NewStore()
	projector := appcatalog.NewProjector(store, fake.NewClock(t1))
	ctx := context.Background()

	if err := projector.ApplyProduct(ctx, appcatalog.ProductEvent{EventID: "e1", Snapshot: snapshot("t1", "p1", 2, t0)}); err != nil {
		t.Fatal(err)
	}

	state, ok := store.Product("t1", "p1")
	if !ok || state.Revision != 2 || state.Version != 1 {
		t.Fatalf("товар: %+v", state)
	}

	rows := store.OutboxRows()
	if len(rows) != 1 || rows[0].Kind != appcatalog.TaskSyncOffer || rows[0].DedupKey != "catalog.offer.sync:t1~p1" {
		t.Fatalf("задача синхронизации предложения: %+v", rows)
	}

	if err := projector.ApplyProduct(ctx, appcatalog.ProductEvent{EventID: "e2", Snapshot: snapshot("t1", "p1", 1, t1)}); err != nil {
		t.Fatal(err)
	}
	if state, _ := store.Product("t1", "p1"); state.Revision != 2 {
		t.Fatal("старая ревизия не применяется")
	}
	if !store.Processed("e2") {
		t.Fatal("пропущенное по ревизии событие всё равно отмечено обработанным")
	}

	if err := projector.ApplyProduct(ctx, appcatalog.ProductEvent{EventID: "e3", Snapshot: snapshot("t1", "p1", 3, t1)}); err != nil {
		t.Fatal(err)
	}
	if rows := store.OutboxRows(); len(rows) != 1 {
		t.Fatalf("невзятая задача по тому же товару склеивается: %+v", rows)
	}
}

func TestApplyProductDeduplicates(t *testing.T) {
	store := fake.NewStore()
	projector := appcatalog.NewProjector(store, fake.NewClock(t1))
	ctx := context.Background()

	if err := projector.ApplyProduct(ctx, appcatalog.ProductEvent{EventID: "e1", Snapshot: snapshot("t1", "p1", 1, t0)}); err != nil {
		t.Fatal(err)
	}

	again := snapshot("t1", "p1", 5, t1)
	again.Price = money.FromKopecks(1)
	if err := projector.ApplyProduct(ctx, appcatalog.ProductEvent{EventID: "e1", Snapshot: again}); err != nil {
		t.Fatal(err)
	}

	if state, _ := store.Product("t1", "p1"); state.Revision != 1 {
		t.Fatal("событие с тем же идентификатором обрабатывается один раз")
	}
}

func TestApplyProductInvalidRollsBack(t *testing.T) {
	store := fake.NewStore()
	projector := appcatalog.NewProjector(store, fake.NewClock(t1))

	bad := snapshot("t1", "p1", 1, t0)
	bad.Card.Name = ""

	err := projector.ApplyProduct(context.Background(), appcatalog.ProductEvent{EventID: "e1", Snapshot: bad})
	if !errors.Is(err, catalog.ErrInvalidSnapshot) {
		t.Fatalf("ожидалось ErrInvalidSnapshot, получено %v", err)
	}
	if store.Processed("e1") || len(store.OutboxRows()) != 0 {
		t.Fatal("отвергнутое событие не оставляет следов")
	}
}

func TestApplyTag(t *testing.T) {
	store := fake.NewStore()
	projector := appcatalog.NewProjector(store, fake.NewClock(t1))
	ctx := context.Background()

	event := appcatalog.TagEvent{EventID: "e1", Snapshot: catalog.TagSnapshot{TenantID: "t1", TagID: "tag-1", Revision: 1, Value: "Новинка"}}
	if err := projector.ApplyTag(ctx, event); err != nil {
		t.Fatal(err)
	}

	update := appcatalog.TagEvent{EventID: "e2", Snapshot: catalog.TagSnapshot{TenantID: "t1", TagID: "tag-1", Revision: 2, Value: "Хит"}}
	if err := projector.ApplyTag(ctx, update); err != nil {
		t.Fatal(err)
	}

	if tag, _ := store.Tag("t1", "tag-1"); tag.Value != "Хит" || tag.Revision != 2 {
		t.Fatalf("тег: %+v", tag)
	}
	if len(store.OutboxRows()) != 0 {
		t.Fatal("теги подписываются при чтении, индекс не трогается")
	}
}

func TestApplyStockAndPrice(t *testing.T) {
	store := fake.NewStore()
	clock := fake.NewClock(t1)
	projector := appcatalog.NewProjector(store, clock)
	ctx := context.Background()

	price := catalog.PriceSignal{Price: money.FromKopecks(9900), At: t1}
	stock := appcatalog.StockEvent{EventID: "e1", TenantID: "t1", ProductID: "p1", Stock: catalog.StockSignal{Available: 4, At: t1}, Price: &price}
	if err := projector.ApplyStock(ctx, stock); err != nil {
		t.Fatal(err)
	}

	signals, ok := store.Signals("t1", "p1")
	if !ok || signals.Available != 4 || signals.Price.Kopecks() != 9900 || signals.Version != 1 {
		t.Fatalf("сигналы: %+v", signals)
	}

	stale := appcatalog.PriceEvent{EventID: "e2", TenantID: "t1", ProductID: "p1", Price: catalog.PriceSignal{Price: money.FromKopecks(1), At: t0}}
	if err := projector.ApplyPrice(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if signals, _ := store.Signals("t1", "p1"); signals.Price.Kopecks() != 9900 {
		t.Fatal("старая цена не применяется")
	}

	fresh := appcatalog.PriceEvent{EventID: "e3", TenantID: "t1", ProductID: "p1", Price: catalog.PriceSignal{Price: money.FromKopecks(12000), At: t2}}
	if err := projector.ApplyPrice(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if signals, _ := store.Signals("t1", "p1"); signals.Price.Kopecks() != 12000 || signals.Version != 2 {
		t.Fatalf("сигналы: %+v", signals)
	}

	if rows := store.OutboxRows(); len(rows) != 1 || rows[0].Kind != appcatalog.TaskSyncOffer {
		t.Fatalf("задача синхронизации склеивается: %+v", rows)
	}

	bad := appcatalog.PriceEvent{EventID: "e4", TenantID: "t1", ProductID: "p1", Price: catalog.PriceSignal{Price: money.FromKopecks(-1), At: t2}}
	if err := projector.ApplyPrice(ctx, bad); !errors.Is(err, catalog.ErrInvalidSignal) {
		t.Fatalf("ожидалось ErrInvalidSignal, получено %v", err)
	}
}
