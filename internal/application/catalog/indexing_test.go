package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

var w3IndexingDelivery = participant.DeliverySettings{Regions: []string{"RU-MOW"}, Carriers: []string{"cdek"}}
var w3IndexingSubsidy = participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed}

type unavailableBrandResolver struct {
	started chan struct{}
}

func (r *unavailableBrandResolver) Resolve(ctx context.Context, _ string) (string, bool, error) {
	close(r.started)
	<-ctx.Done()

	return "", false, ctx.Err()
}

func indexer(store *fake.Store, index *fake.Index, clock *fake.Clock) *appcatalog.Indexer {
	return appcatalog.NewIndexer(store, index, clock, 2, &fake.IndexRebuildLock{}, nil)
}

func TestSyncOfferWritesParticipantOffers(t *testing.T) {
	store, index, clock := fake.NewStore(), fake.NewIndex(), fake.NewClock(t1)
	seedSeller(store, "t1", participant.TenantActive, true)
	seedProduct(store, "t1", "p1")
	seedProduct(store, "outsider", "p1")
	ctx := context.Background()

	if err := indexer(store, index, clock).SyncOffer(ctx, "t1", "p1"); err != nil {
		t.Fatal(err)
	}

	doc, ok := index.Doc("t1~p1")
	if len(doc.Categories) != 1 || doc.Categories[0].Name != "Полное имя категории" {
		t.Fatalf("полное имя категории не попало в индекс: %+v", doc.Categories)
	}
	if !ok || !doc.Visible || !doc.Listed || !doc.CheckoutAvailable || doc.Price != 10000 ||
		doc.SellerSlug != "slug-t1" {
		t.Fatalf("документ: %+v", doc)
	}
	if doc.Version != 4 {
		t.Fatalf("версия — сумма версий товара, участника и реестра: %d", doc.Version)
	}

	if err := indexer(store, index, clock).SyncOffer(ctx, "outsider", "p1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := index.Doc("outsider~p1"); ok {
		t.Fatal("товар магазина без участия в индекс не попадает")
	}

	if err := indexer(store, index, clock).SyncOffer(ctx, "t1", "missing"); err != nil {
		t.Fatalf("товара нет — синхронизировать нечего: %v", err)
	}
}

func TestSyncOfferHiddenSellerLeavesTombstone(t *testing.T) {
	store, index, clock := fake.NewStore(), fake.NewIndex(), fake.NewClock(t1)
	seedSeller(store, "t1", participant.TenantSuspended, true)
	seedProduct(store, "t1", "p1")

	if err := indexer(store, index, clock).SyncOffer(context.Background(), "t1", "p1"); err != nil {
		t.Fatal(err)
	}

	doc, ok := index.Doc("t1~p1")
	if !ok || doc.Visible || doc.Listed || doc.Name != "" || doc.Price != 0 {
		t.Fatalf("скрытое предложение — документ без карточки: %+v", doc)
	}
}

func TestSyncTenantPagesThroughProducts(t *testing.T) {
	store, index, clock := fake.NewStore(), fake.NewIndex(), fake.NewClock(t1)
	seedSeller(store, "t1", participant.TenantActive, true)
	for _, id := range []ids.ProductID{"p1", "p2", "p3", "p4", "p5"} {
		seedProduct(store, "t1", id)
	}

	if err := indexer(store, index, clock).SyncTenant(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}

	page, _ := index.Search(context.Background(), ports.OfferQuery{Limit: 10})
	if page.Total != 5 {
		t.Fatalf("все товары магазина в индексе: %d", page.Total)
	}
}

func TestStaleDocumentDoesNotOverwrite(t *testing.T) {
	store, index, clock := fake.NewStore(), fake.NewIndex(), fake.NewClock(t1)
	seedSeller(store, "t1", participant.TenantActive, true)
	seedProduct(store, "t1", "p1")
	ctx := context.Background()

	var stale ports.OfferState
	_ = store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		stale, err = tx.Offers().Offer(ctx, "t1", "p1")

		return err
	})

	p, _ := store.Participant("t1")
	p.Enabled = false
	p.Stage = participant.StageDisconnected
	p.Version = 2
	store.SeedParticipant(p)

	if err := indexer(store, index, clock).SyncOffer(ctx, "t1", "p1"); err != nil {
		t.Fatal(err)
	}

	staleDoc, _ := appcatalog.Document(stale)
	if err := index.Put(ctx, []ports.OfferDocument{staleDoc}); err != nil {
		t.Fatal(err)
	}

	if doc, _ := index.Doc("t1~p1"); doc.Visible {
		t.Fatal("документ, прочитанный до выключения участника, не перезаписывает свежий")
	}
}

func TestDocumentVersionIncludesSignals(t *testing.T) {
	product, _ := catalog.NewProduct(snapshot("t1", "p1", 1, t0), t0)
	product.MarkSaved()
	signals := catalog.NewSignals("t1", "p1")
	_, _ = signals.ApplyStock(catalog.StockSignal{Available: 0, At: t1}, t1)
	_, _ = signals.ApplyPrice(catalog.PriceSignal{Price: money.FromKopecks(5000), At: t1}, t1)

	tenant, _ := participant.NewTenant(
		participant.RegistryEvent{TenantID: "t1", Slug: "s", Status: participant.TenantActive, At: t0},
		t0,
	)
	owner, _ := participant.New(tenant, "Магазин", t0)
	owner.Enable(t0)
	if _, err := owner.ApplyShopSettings(w3IndexingSubsidy, money.Money{}, w3IndexingDelivery, t0); err != nil {
		t.Fatal(err)
	}
	state := ports.OfferState{
		Verdict:     moderation.RestoreVerdict(moderation.VerdictState{Status: moderation.Allowed, Version: 1}),
		Product:     product,
		Signals:     signals,
		Participant: owner,
		Tenant:      tenant,
	}

	doc, ok := appcatalog.Document(state)
	if !ok || doc.Version != 5 || doc.Price != 10000 || doc.InStock || !doc.Visible || !doc.CheckoutAvailable {
		t.Fatalf("документ: %+v", doc)
	}
	if !doc.UpdatedAt.Equal(t1) {
		t.Fatalf("время обновления — самое позднее из товара и сигналов: %s", doc.UpdatedAt)
	}
}

func TestRebuildCreatesGenerationAndCatchesUp(t *testing.T) {
	store, index, clock := fake.NewStore(), fake.NewIndex(), fake.NewClock(t1)
	seedSeller(store, "t1", participant.TenantActive, true)
	seedSeller(store, "t2", participant.TenantActive, false)
	seedProduct(store, "t1", "p1")
	seedProduct(store, "t1", "p2")
	seedProduct(store, "t1", "p3")
	seedProduct(store, "t2", "p1")
	seedProduct(store, "outsider", "p1")
	ctx := context.Background()

	if err := index.Put(ctx, []ports.OfferDocument{
		{
			ID:      "garbage~x",
			Listed:  true,
			Visible: true,
			PriceProfiles: []catalog.PriceProfile{
				{
					Key:            catalog.DefaultPriceProfile,
					Price:          money.FromKopecks(0),
					EffectivePrice: money.FromKopecks(0),
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	result, err := indexer(store, index, clock).Rebuild(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Indexed != 4 || index.Active() != result.Generation {
		t.Fatalf("перестроение: %+v, рабочее %s", result, index.Active())
	}
	if _, ok := index.Doc("garbage~x"); ok {
		t.Fatal("прежнее поколение удалено")
	}
	if doc, _ := index.Doc("t2~p1"); doc.Visible {
		t.Fatal("выключенный участник — документ без карточки")
	}
	if result.CaughtUp != 4 {
		t.Fatalf("всё, что менялось в окне запаса, догоняется: %+v", result)
	}

	clock.Advance(time.Hour)
	result, err = indexer(store, index, clock).Rebuild(ctx)
	if err != nil || result.CaughtUp != 0 {
		t.Fatalf("без изменений догонять нечего: %+v, %v", result, err)
	}
}

func TestEnsureIndex(t *testing.T) {
	store, index, clock := fake.NewStore(), fake.NewIndex(), fake.NewClock(t1)
	seedSeller(store, "t1", participant.TenantActive, true)
	seedProduct(store, "t1", "p1")
	ctx := context.Background()

	built, err := indexer(store, index, clock).EnsureIndex(ctx)
	if err != nil || !built {
		t.Fatalf("нет индекса — собирается: %v, %v", built, err)
	}
	if _, ok := index.Doc("t1~p1"); !ok {
		t.Fatal("документ после сборки")
	}

	built, err = indexer(store, index, clock).EnsureIndex(ctx)
	if err != nil || built {
		t.Fatalf("есть индекс — не трогается: %v, %v", built, err)
	}
}

func TestHandleTask(t *testing.T) {
	store, index, clock := fake.NewStore(), fake.NewIndex(), fake.NewClock(t1)
	seedSeller(store, "t1", participant.TenantActive, true)
	seedProduct(store, "t1", "p1")
	ctx := context.Background()
	idx := indexer(store, index, clock)

	if err := idx.HandleTask(ctx, taskOf(appcatalog.SyncOfferTask("t1", "p1", t1))); err != nil {
		t.Fatal(err)
	}
	if _, ok := index.Doc("t1~p1"); !ok {
		t.Fatal("задача предложения выполнена")
	}
	tenantTask := taskOf(appcatalog.SyncTenantTask("t1", t1))
	if err := idx.HandleTask(ctx, tenantTask); err != nil {
		t.Fatal(err)
	}

	mismatch, _ := json.Marshal(map[string]string{"tenantId": "t2", "productId": "p1"})
	err := idx.HandleTask(ctx, ports.Task{Kind: appcatalog.TaskSyncOffer, TenantID: "t1", Payload: mismatch})
	if !errors.Is(err, appcatalog.ErrInvalidTask) {
		t.Fatalf("магазин задачи не совпадает: получено %v", err)
	}

	err = idx.HandleTask(ctx, ports.Task{Kind: appcatalog.TaskSyncOffer, TenantID: "t1", Payload: []byte("{")})
	if !errors.Is(err, appcatalog.ErrInvalidTask) {
		t.Fatalf("повреждённая задача: получено %v", err)
	}

	tenantTask.Kind = "other"

	err = idx.HandleTask(ctx, tenantTask)
	if !errors.Is(err, appcatalog.ErrInvalidTask) {
		t.Fatalf("неизвестная задача: получено %v", err)
	}
}

func TestSyncOfferPropagatesIndexFailure(t *testing.T) {
	store, index, clock := fake.NewStore(), fake.NewIndex(), fake.NewClock(t1)
	seedSeller(store, "t1", participant.TenantActive, true)
	seedProduct(store, "t1", "p1")
	index.FailPut = errors.New("кластер недоступен")

	if err := indexer(store, index, clock).SyncOffer(context.Background(), "t1", "p1"); err == nil {
		t.Fatal("сбой индекса возвращается, задача уйдёт на повтор")
	}
}

// taskOf — задача outbox в том виде, в каком её отдаёт очередь.
func taskOf(task ports.NewTask) ports.Task {
	return ports.Task{
		Kind:     task.Kind,
		TenantID: task.TenantID,
		Payload:  task.Payload,
	}
}

func TestProjectionContinuesWhileBrandRegistryIsUnavailable(t *testing.T) {
	store, index, clock := fake.NewStore(), fake.NewIndex(), fake.NewClock(t1)
	seedSeller(store, "t1", participant.TenantActive, true)
	snap := snapshot("t1", "p1", 1, t0)
	snap.Card.Publication.Attributes = map[string]json.RawMessage{
		"brand":       json.RawMessage(`"Unavailable"`),
		"part_number": json.RawMessage(`"A1"`),
	}
	product, err := catalog.NewProduct(snap, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Products().Save(ctx, product)
	}); err != nil {
		t.Fatal(err)
	}
	resolver := &unavailableBrandResolver{started: make(chan struct{})}
	indexer := appcatalog.NewIndexer(
		store,
		index,
		clock,
		1,
		&fake.IndexRebuildLock{},
		appcatalog.NewModelKeys(resolver, nil, true),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- indexer.SyncOffer(ctx, "t1", "p1")
	}()
	<-resolver.started

	// Пока индексатор ждёт реестр, проекция обрабатывает следующее событие.
	projectionCtx, stop := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stop()
	snap.Revision = 2
	projector := appcatalog.NewProjector(store, clock, nil)
	if err := projector.ApplyProduct(projectionCtx, productEvent("next", snap)); err != nil {
		t.Fatal(err)
	}
	if state, _ := store.Product("t1", "p1"); state.Revision != 2 {
		t.Fatal("проекция ждёт реестр вместо сохранения события")
	}
	cancel()
	<-done
}
