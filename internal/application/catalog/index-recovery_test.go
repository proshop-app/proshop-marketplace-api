package catalog_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type recoverableIndex struct {
	*fake.Index
	building string
	put      func([]ports.OfferDocument) error
}

type indexSkips map[string]int

func (s indexSkips) ObserveIndexSkips(phase string, count int) {
	s[phase] += count
}

func (x *recoverableIndex) NewGeneration(ctx context.Context) (string, error) {
	if x.building == "" {
		var err error
		x.building, err = x.Index.NewGeneration(ctx)
		return x.building, err
	}
	return x.building, nil
}

func (x *recoverableIndex) PutGeneration(ctx context.Context, generation string, docs []ports.OfferDocument) error {
	if err := x.put(docs); err != nil {
		return err
	}
	return x.Index.PutGeneration(ctx, generation, docs)
}

func TestRebuildResumesAfterAnIsolatedRejectionAndTemporaryFailure(t *testing.T) {
	store, clock := fake.NewStore(), fake.NewClock(t1.Add(time.Hour))
	ctx := context.Background()
	seedSeller(store, "t1", participant.TenantActive, true)
	seedSeller(store, "t2", participant.TenantActive, true)
	for _, id := range []ids.ProductID{"p1", "p2", "p3"} {
		seedProduct(store, "t1", id)
	}
	seedProduct(store, "t2", "p1")

	poison := errs.New(errs.Invalid, "test.rejected", "поиск отклонил документ")
	unavailable := errors.New("индекс временно недоступен")
	var attempts []string
	failed := false
	index := &recoverableIndex{
		Index: fake.NewIndex(),
		put: func(docs []ports.OfferDocument) error {
			attempts = append(attempts, docs[0].ID)
			for _, doc := range docs {
				if doc.ID == "t1~p2" {
					return poison
				}
			}
			if docs[0].ID == "t1~p3" && !failed {
				failed = true
				return unavailable
			}
			return nil
		},
	}
	lock, skips := &fake.IndexRebuildLock{}, indexSkips{}
	service := appcatalog.NewIndexer(store, index, clock, 2, lock, nil, time.Second)
	service.SetSkipObserver(skips)
	first, err := service.Rebuild(ctx)
	if !errors.Is(err, unavailable) || first.Indexed != 1 || first.Skipped != 1 || index.Active() != "" {
		t.Fatalf("прерванный обход: %+v, %v", first, err)
	}

	replica := appcatalog.NewIndexer(store, index, clock, 2, lock, nil, time.Second)
	replica.SetSkipObserver(skips)
	resumed, err := replica.Rebuild(ctx)
	if err != nil || resumed.Generation != first.Generation || resumed.Indexed != 3 || resumed.Skipped != 1 {
		t.Fatalf("повтор поколения: %+v, %v", resumed, err)
	}
	if want := []string{"t1~p1", "t1~p1", "t1~p2", "t1~p3", "t1~p3"}; !reflect.DeepEqual(attempts, want) {
		t.Fatalf("попытки записи: %v, ожидалось %v", attempts, want)
	}
	if _, ok := index.Doc("t2~p1"); !ok {
		t.Fatal("последняя карточка не попала в новое поколение")
	}
	if _, ok := index.Doc("t1~p2"); ok || skips["rebuild"] != 1 {
		t.Fatalf("результат отклонённой карточки или счётчик: %v, %v", ok, skips)
	}
}

func TestTenantIndexContinuesPastRejectionAndCoalescesNewTarget(t *testing.T) {
	store, index, clock := fake.NewStore(), fake.NewIndex(), fake.NewClock(t1)
	ctx := context.Background()
	seedSeller(store, "t1", participant.TenantActive, true)
	for _, id := range []ids.ProductID{"p1", "p2", "p3"} {
		seedProduct(store, "t1", id)
	}
	poison := errs.New(errs.Invalid, "test.rejected", "поиск отклонил документ")
	index.RejectPut = func(docs []ports.OfferDocument) error {
		for _, doc := range docs {
			if doc.ID == "t1~p1" && doc.Visible {
				return poison
			}
		}
		return nil
	}

	service := indexer(store, index, clock)
	skips := indexSkips{}
	service.SetSkipObserver(skips)
	task := taskOf(appcatalog.SyncTenantTask("t1", t1))
	if err := service.HandleTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	seller, _ := store.Participant("t1")
	seller.Stage = participant.StageDisconnected
	seller.Enabled = false
	seller.Version += 10
	store.SeedParticipant(seller)
	if err := service.HandleTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if doc, ok := index.Doc("t1~p3"); !ok || doc.Visible {
		t.Fatal("сигнал о новой версии должен сначала дать закончить предыдущий обход")
	}

	for range 4 {
		if err := service.HandleTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	var attempts []string
	for _, docs := range index.PutBatches {
		attempts = append(attempts, docs[0].ID)
	}
	want := []string{"t1~p1", "t1~p1", "t1~p2", "t1~p3", "t1~p1", "t1~p3"}
	if !reflect.DeepEqual(attempts, want) || skips["tenant"] != 1 {
		t.Fatalf("попытки записи: %v, ожидалось %v, счётчик: %v", attempts, want, skips)
	}
	if doc, ok := index.Doc("t1~p2"); !ok || doc.Visible {
		t.Fatal("первая страница должна отразить последнюю версию магазина")
	}
}

func TestTenantIndexKeepsCursorOnTemporaryFailure(t *testing.T) {
	store, index, clock := fake.NewStore(), fake.NewIndex(), fake.NewClock(t1)
	ctx := context.Background()
	seedSeller(store, "t1", participant.TenantActive, true)
	for _, id := range []ids.ProductID{"p1", "p2", "p3"} {
		seedProduct(store, "t1", id)
	}

	unavailable := errors.New("потерян ответ поиска")
	index.RejectPut = func(docs []ports.OfferDocument) error {
		if docs[0].ID == "t1~p3" {
			return unavailable
		}
		return nil
	}
	task := taskOf(appcatalog.SyncTenantTask("t1", t1))
	service := indexer(store, index, clock)
	if err := service.HandleTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleTask(ctx, task); !errors.Is(err, unavailable) {
		t.Fatal(err)
	}
	index.RejectPut = nil
	if err := service.HandleTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if len(index.PutBatches) != 3 || index.PutBatches[2][0].ID != "t1~p3" {
		t.Fatalf("запись должна продолжиться со второй страницы: %v", index.PutBatches)
	}
}
