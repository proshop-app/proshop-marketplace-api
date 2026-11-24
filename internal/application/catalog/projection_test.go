package catalog_test

import (
	"context"
	"errors"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestApplyProduct(t *testing.T) {
	store := fake.NewStore()
	projector := appcatalog.NewProjector(store, fake.NewClock(t1), nil)
	ctx := context.Background()

	if err := projector.ApplyProduct(ctx, productEvent("e1", snapshot("t1", "p1", 2, t0))); err != nil {
		t.Fatal(err)
	}

	state, ok := store.Product("t1", "p1")
	if !ok || state.Revision != 2 || state.Version != 1 {
		t.Fatalf("товар: %+v", state)
	}

	rows := store.OutboxRows()
	if len(rows) != 2 || rows[1].Kind != appcatalog.TaskSyncOffer || rows[1].DedupKey != "catalog.offer.sync:t1~p1" {
		t.Fatalf("задача синхронизации предложения: %+v", rows)
	}

	if rows[0].Kind != "moderation.offer.status" || rows[0].DedupKey != "moderation.offer.status:t1~p1" {
		t.Fatal("нет задачи статуса", rows)
	}

	if err := projector.ApplyProduct(ctx, productEvent("e2", snapshot("t1", "p1", 1, t1))); err != nil {
		t.Fatal(err)
	}
	if state, _ := store.Product("t1", "p1"); state.Revision != 2 {
		t.Fatal("старая ревизия не применяется")
	}
	// Повтор и старый снимок отсекает ревизия: отметка processed_events не пишется.
	if store.Processed("e1") || store.Processed("e2") {
		t.Fatal("снимок товара не отмечается в processed_events")
	}

	if err := projector.ApplyProduct(ctx, productEvent("e3", snapshot("t1", "p1", 3, t1))); err != nil {
		t.Fatal(err)
	}
	if rows := store.OutboxRows(); len(rows) != 2 {
		t.Fatalf("невзятая задача по тому же товару склеивается: %+v", rows)
	}
}

// Повторная доставка того же сообщения — та же ревизия: запись и очередь не меняются.
func TestApplyProductRedeliveryIsNoop(t *testing.T) {
	store := fake.NewStore()
	projector := appcatalog.NewProjector(store, fake.NewClock(t1), nil)
	ctx := context.Background()
	event := productEvent("e1", snapshot("t1", "p1", 1, t0))

	if err := projector.ApplyProduct(ctx, event); err != nil {
		t.Fatal(err)
	}
	queued := len(store.OutboxRows())
	event.Snapshot.Price = money.FromKopecks(1)
	if err := projector.ApplyProduct(ctx, event); err != nil {
		t.Fatal(err)
	}

	if state, _ := store.Product("t1", "p1"); state.Version != 1 || state.Price.Kopecks() == 1 {
		t.Fatal("повтор той же ревизии изменил запись", state)
	}
	if len(store.OutboxRows()) != queued {
		t.Fatal("повтор той же ревизии поставил задачи", store.OutboxRows())
	}
}

// Пачка применяет снимки разных товаров одной транзакцией, а сбой одного откатывает все.
func TestApplyProductsBatchIsAtomic(t *testing.T) {
	store := fake.NewStore()
	projector := appcatalog.NewProjector(store, fake.NewClock(t1), nil)
	ctx := context.Background()
	batch := []appcatalog.ProductEvent{
		productEvent("e1", snapshot("t1", "p1", 1, t0)),
		productEvent("e2", snapshot("t2", "p2", 1, t0)),
	}
	if err := projector.ApplyProducts(ctx, batch); err != nil {
		t.Fatal(err)
	}
	for _, key := range [][2]string{{"t1", "p1"}, {"t2", "p2"}} {
		tenant, product := ids.TenantID(key[0]), ids.ProductID(key[1])
		if state, ok := store.Product(tenant, product); !ok || state.Revision != 1 {
			t.Fatal("товар пачки не применён", key, state)
		}
	}
	if rows := store.OutboxRows(); len(rows) != 4 {
		t.Fatal("задачи пачки", rows)
	}

	bad := snapshot("t1", "p3", 1, t0)
	bad.Card.Name = ""
	failed := []appcatalog.ProductEvent{productEvent("e3", snapshot("t2", "p4", 1, t0)), productEvent("e4", bad)}
	if err := projector.ApplyProducts(ctx, failed); err == nil {
		t.Fatal("невалидный снимок пачки должен вернуть ошибку")
	}
	if _, ok := store.Product("t2", "p4"); ok {
		t.Fatal("сбой одного снимка откатывает пачку")
	}
	if rows := store.OutboxRows(); len(rows) != 4 {
		t.Fatal("задачи откатанной пачки", rows)
	}
}

func TestApplyProductInvalidRollsBack(t *testing.T) {
	store := fake.NewStore()
	projector := appcatalog.NewProjector(store, fake.NewClock(t1), nil)

	bad := snapshot("t1", "p1", 1, t0)
	bad.Card.Name = ""

	err := projector.ApplyProduct(context.Background(), appcatalog.ProductEvent{Snapshot: bad})
	if !errors.Is(err, catalog.ErrInvalidSnapshot) {
		t.Fatalf("ожидалось ErrInvalidSnapshot, получено %v", err)
	}
	if store.Processed("e1") || len(store.OutboxRows()) != 0 {
		t.Fatal("отвергнутое событие не оставляет следов")
	}
}

func TestApplyStockAndPrice(t *testing.T) {
	store := fake.NewStore()
	clock := fake.NewClock(t1)
	projector := appcatalog.NewProjector(store, clock, nil)
	ctx := context.Background()

	price := catalog.PriceSignal{Price: money.FromKopecks(9900), At: t1}
	stock := appcatalog.StockEvent{
		TenantID:  "t1",
		ProductID: "p1",
		Stock:     catalog.StockSignal{Available: 4, At: t1},
		Price:     &price,
	}
	if err := projector.ApplyStock(ctx, stock); err != nil {
		t.Fatal(err)
	}

	signals, ok := store.Signals("t1", "p1")
	if !ok || signals.Available != 4 || signals.Price.Kopecks() != 9900 || signals.Version != 1 {
		t.Fatalf("сигналы: %+v", signals)
	}

	stale := appcatalog.PriceEvent{
		EventID:   "e2",
		TenantID:  "t1",
		ProductID: "p1",
		Price:     catalog.PriceSignal{Price: money.FromKopecks(1), At: t0},
	}
	if err := projector.ApplyPrice(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if signals, _ := store.Signals("t1", "p1"); signals.Price.Kopecks() != 9900 {
		t.Fatal("старая цена не применяется")
	}

	fresh := appcatalog.PriceEvent{
		EventID:   "e3",
		TenantID:  "t1",
		ProductID: "p1",
		Price:     catalog.PriceSignal{Price: money.FromKopecks(12000), At: t2},
	}
	if err := projector.ApplyPrice(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if signals, _ := store.Signals("t1", "p1"); signals.Price.Kopecks() != 12000 || signals.Version != 2 {
		t.Fatalf("сигналы: %+v", signals)
	}

	if rows := store.OutboxRows(); len(rows) != 1 || rows[0].Kind != appcatalog.TaskSyncOffer {
		t.Fatalf("задача синхронизации склеивается: %+v", rows)
	}

	bad := appcatalog.PriceEvent{
		EventID:   "e4",
		TenantID:  "t1",
		ProductID: "p1",
		Price:     catalog.PriceSignal{Price: money.FromKopecks(-1), At: t2},
	}
	if err := projector.ApplyPrice(ctx, bad); !errors.Is(err, catalog.ErrInvalidSignal) {
		t.Fatalf("ожидалось ErrInvalidSignal, получено %v", err)
	}
}
