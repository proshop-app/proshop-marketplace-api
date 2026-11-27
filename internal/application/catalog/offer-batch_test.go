package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func batchDispatcher(store *fake.Store, index *fake.Index, clock *fake.Clock) *messaging.Dispatcher {
	d := messaging.NewDispatcher(
		store,
		clock,
		messaging.DispatcherConfig{
			Batch:     200,
			PerTenant: 200,
			Lease:     time.Minute,
		},
		nil,
	)
	d.RegisterBatch(appcatalog.TaskSyncOffer, indexer(store, index, clock).HandleOfferBatch)
	return d
}

func enqueueOffer(t *testing.T, store *fake.Store, tenant ids.TenantID, product ids.ProductID) {
	t.Helper()
	err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		task := appcatalog.SyncOfferTask(tenant, product, t1)
		// Две отдельные задачи возможны после захвата старой задачи обработчиком.
		task.DedupKey = ""
		return tx.Outbox().Enqueue(ctx, task)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func seedOfferBatch(t *testing.T, size int) (*fake.Store, *fake.Index, *fake.Clock) {
	t.Helper()
	store, index, clock := fake.NewStore(), fake.NewIndex(), fake.NewClock(t1)
	seedSeller(store, "t1", participant.TenantActive, true)
	for n := range size {
		product := ids.ProductID(fmt.Sprintf("p%d", n))
		seedProduct(store, "t1", product)
		enqueueOffer(t, store, "t1", product)
	}
	return store, index, clock
}

func TestOfferBatchUsesOneIndexCall(t *testing.T) {
	store, index, clock := seedOfferBatch(t, 200)
	outcomes, _, _, err := batchDispatcher(store, index, clock).RunWork(context.Background(), nil)
	if err != nil || len(outcomes) != 200 || len(index.PutBatches) != 1 || len(index.PutBatches[0]) != 200 {
		t.Fatalf("200 задач должны дать одну отправку: %d, %d, %v", len(outcomes), len(index.PutBatches), err)
	}
	for _, row := range store.OutboxRows() {
		if row.Kind == appcatalog.TaskSyncOffer && row.DoneAt == nil {
			t.Fatal("успешная задача не завершена")
		}
		if row.Kind == ports.TaskStoreCard {
			t.Fatal("синхронизация индекса поставила задачу сводки")
		}
	}
}

func TestOfferBatchReadsLatestAndCoalescesTasks(t *testing.T) {
	store, index, clock := seedOfferBatch(t, 1)
	enqueueOffer(t, store, "t1", "p0")
	state, _ := store.Product("t1", "p0")
	state.Card.Name = "Новое название"
	state.Revision++
	state.Version++
	store.SeedProduct(state)
	outcomes, _, _, err := batchDispatcher(store, index, clock).RunWork(context.Background(), nil)
	if err != nil || len(outcomes) != 2 || len(index.PutBatches) != 1 || len(index.PutBatches[0]) != 1 {
		t.Fatalf("две задачи одного предложения склеиваются: %+v, %v", outcomes, err)
	}
	doc := index.PutBatches[0][0]
	if doc.Name != state.Card.Name || doc.Version != 5 {
		t.Fatalf("нужен актуальный документ: %+v", doc)
	}
	// Запоздалое подтверждение или повтор старой версии не возвращает старую карточку.
	stale := doc
	stale.Version--
	stale.Name = "Старое название"
	if err := index.Put(context.Background(), []ports.OfferDocument{stale}); err != nil {
		t.Fatal(err)
	}
	if saved, _ := index.Doc(doc.ID); saved.Name != doc.Name {
		t.Fatal("старая версия затёрла новую")
	}
}

func TestOfferBatchRetriesWholeFailureWithBackoff(t *testing.T) {
	store, index, clock := seedOfferBatch(t, 4)
	index.FailPut = errors.New("поиск недоступен")
	d := batchDispatcher(store, index, clock)
	outcomes, _, _, err := d.RunWork(context.Background(), nil)
	if err != nil || len(outcomes) != 4 || len(index.PutBatches) != 1 {
		t.Fatalf("временный сбой не дробит пачку: %+v, %v", outcomes, err)
	}
	for _, row := range store.OutboxRows() {
		if row.Kind == appcatalog.TaskSyncOffer {
			if row.DoneAt != nil || row.LastError == "" || !row.NextAt.Equal(t1.Add(time.Second)) {
				t.Fatalf("все задачи должны быть отложены: %+v", row)
			}
		}
	}
	if outcomes, _, _, err := d.RunWork(context.Background(), nil); err != nil || len(outcomes) != 0 {
		t.Fatalf("повтор до срока: %+v, %v", outcomes, err)
	}
	clock.Advance(time.Second)
	index.FailPut = nil
	if outcomes, _, _, err := d.RunWork(context.Background(), nil); err != nil || len(outcomes) != 4 {
		t.Fatalf("повтор всей пачки: %+v, %v", outcomes, err)
	}
	if len(index.PutBatches) != 2 || len(index.PutBatches[1]) != 4 {
		t.Fatal("повтор должен доставить все документы одним вызовом")
	}
}

func TestOfferBatchDefersOnlyRejectedOffer(t *testing.T) {
	store, index, clock := seedOfferBatch(t, 2)
	enqueueOffer(t, store, "t1", "p0")
	index.RejectPut = func(docs []ports.OfferDocument) error {
		return &ports.OfferCapacityError{
			Deferred:        []ids.OfferID{{Tenant: "t1", Product: "p0"}},
			UnconfirmedFrom: len(docs),
		}
	}
	d := batchDispatcher(store, index, clock)
	ctx := context.Background()
	outcomes, _, _, err := d.RunWork(ctx, nil)
	if err != nil || len(outcomes) != 3 {
		t.Fatalf("первый проход: %+v, %v", outcomes, err)
	}
	for _, row := range store.OutboxRows() {
		if row.Kind != appcatalog.TaskSyncOffer {
			continue
		}
		if row.DoneAt == nil && (!row.NextAt.Equal(t1.Add(time.Second)) || row.LastError == "") {
			t.Fatalf("отклонённая задача должна ждать: %+v", row)
		}
	}
	seedSeller(store, "t2", participant.TenantActive, true)
	seedProduct(store, "t2", "p0")
	enqueueOffer(t, store, "t2", "p0")
	index.RejectPut = nil
	outcomes, _, _, err = d.RunWork(ctx, nil)
	if err != nil || len(outcomes) != 1 || outcomes[0].Task.TenantID != "t2" || outcomes[0].Err != nil {
		t.Fatalf("новое предложение другого продавца: %+v, %v", outcomes, err)
	}
	clock.Advance(time.Second)
	outcomes, _, _, err = d.RunWork(ctx, nil)
	if err != nil || len(outcomes) != 2 || len(index.PutBatches) != 3 {
		t.Fatalf("повтор двух задач одного предложения: %+v, %v", outcomes, err)
	}
	if retry := index.PutBatches[2]; len(retry) != 1 || retry[0].ID != "t1~p0" {
		t.Fatalf("повтор отправил лишние документы: %v", retry)
	}
}

func TestOfferBatchIsolatesPoison(t *testing.T) {
	store, index, clock := seedOfferBatch(t, 5)
	index.RejectPut = func(docs []ports.OfferDocument) error {
		for _, doc := range docs {
			if doc.ProductID == "p2" {
				return errs.New(errs.Invalid, "search.invalid_request", "поиск отверг документ")
			}
		}
		return nil
	}
	outcomes, _, _, err := batchDispatcher(store, index, clock).RunWork(context.Background(), nil)
	if err != nil || len(outcomes) != 5 {
		t.Fatal(outcomes, err)
	}
	failed := 0
	for _, outcome := range outcomes {
		if outcome.Err != nil {
			failed++
			if errs.CodeOf(outcome.Err) != errs.Invalid {
				t.Fatal(outcome.Err)
			}
		}
	}
	if failed != 1 {
		t.Fatalf("отказ должен остаться только у отравленного документа: %d", failed)
	}
	for n := range 5 {
		_, exists := index.Doc(fmt.Sprintf("t1~p%d", n))
		if exists != (n != 2) {
			t.Fatalf("неверный итог предложения p%d", n)
		}
	}
	for _, row := range store.OutboxRows() {
		if row.Kind == appcatalog.TaskSyncOffer && row.DoneAt == nil && row.LastError == "" {
			t.Fatal("отравленная задача должна сохранить причину повтора")
		}
	}
}

func TestOfferBatchIncludesHiddenAndDeletedDocuments(t *testing.T) {
	store, index, clock := seedOfferBatch(t, 3)
	for _, product := range []ids.ProductID{"p0", "p1"} {
		state, _ := store.Product("t1", product)
		state.Version++
		if product == "p0" {
			state.Deleted = true
		} else {
			state.Card.Visible = false
		}
		store.SeedProduct(state)
	}
	if _, _, _, err := batchDispatcher(store, index, clock).RunWork(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(index.PutBatches) != 1 || len(index.PutBatches[0]) != 3 {
		t.Fatal("скрытие должно доставляться общей пачкой")
	}
	for _, product := range []string{"p0", "p1"} {
		doc, found := index.Doc("t1~" + product)
		if !found || doc.Visible || doc.Name != "" {
			t.Fatalf("нужен tombstone: %+v", doc)
		}
	}
}
