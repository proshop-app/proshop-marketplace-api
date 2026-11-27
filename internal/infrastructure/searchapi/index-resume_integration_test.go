//go:build integration

package searchapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type indexRecoveryProbe struct {
	ports.OfferIndex
	before func([]ports.OfferDocument) error
}

type heldIndexActivation struct {
	*fake.Index
	entered chan struct{}
	release chan struct{}
}

type indexSkipCounts map[string]int

func (s indexSkipCounts) ObserveIndexSkips(phase string, count int) {
	s[phase] += count
}

func (x indexRecoveryProbe) PutGeneration(
	ctx context.Context,
	generation string,
	docs []ports.OfferDocument,
) error {
	if err := x.before(docs); err != nil {
		return err
	}
	return x.OfferIndex.PutGeneration(ctx, generation, rejectOneDocument(docs))
}

func (x indexRecoveryProbe) Put(ctx context.Context, docs []ports.OfferDocument) error {
	if err := x.before(docs); err != nil {
		return err
	}
	return x.OfferIndex.Put(ctx, rejectOneDocument(docs))
}

// Замена на границе адаптера проверяет реальный HTTP-отказ валидации поиска.
func rejectOneDocument(docs []ports.OfferDocument) []ports.OfferDocument {
	for n, doc := range docs {
		if doc.ProductID == "p02" && doc.Visible {
			changed := slices.Clone(docs)
			changed[n].Name = ""
			return changed
		}
	}
	return docs
}

func (x heldIndexActivation) Activate(ctx context.Context, generation string) error {
	close(x.entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-x.release:
		return x.Index.Activate(ctx, generation)
	}
}

func TestIndexActivationLeavesCatalogWritesAvailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	clock := fake.NewClock(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	identity := &fake.IDs{}
	seedIndexRecoveryCatalog(t, uow, clock, identity)
	index := heldIndexActivation{
		Index:   fake.NewIndex(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	lock := postgres.NewIndexRebuildLock(postgres.DirectSessionsOf(pool))
	indexer := appcatalog.NewIndexer(uow, index, clock, 100, lock, nil, time.Minute)
	finished := make(chan struct{})
	var rebuildErr error
	go func() {
		defer close(finished)
		_, rebuildErr = indexer.Rebuild(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-finished
	})
	select {
	case <-index.entered:
	case <-finished:
		t.Fatalf("пересборка завершилась до активации: %v", rebuildErr)
	}

	moderation := appmoderation.NewService(uow, clock, identity, appmoderation.Config{ProbationOffers: 50})
	projector := appcatalog.NewProjector(uow, clock, moderation)
	event := appcatalog.ProductEvent{Snapshot: recoverySnapshot("p21", clock.Now())}
	if err := projector.ApplyProduct(ctx, event); err != nil {
		t.Fatal(err)
	}
	close(index.release)
	<-finished
	if rebuildErr != nil {
		t.Fatal(rebuildErr)
	}
}

func TestIndexRecoveryWithSearch(t *testing.T) {
	address := os.Getenv("TEST_INDEX_RECOVERY_SEARCH_API_URL")
	if address == "" {
		t.Skip("нужен отдельный процесс поиска в TEST_INDEX_RECOVERY_SEARCH_API_URL")
	}
	if address == os.Getenv("TEST_SEARCH_API_URL") {
		t.Fatal("поиск для восстановления должен иметь собственное поколение")
	}
	t.Setenv("TEST_SEARCH_API_URL", address)
	index := testSearchIndex(t)
	ctx := context.Background()
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	past := fake.NewClock(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	identity := &fake.IDs{}
	seedIndexRecoveryCatalog(t, uow, past, identity)
	clock := fake.NewClock(past.Now().Add(time.Hour))
	lock := postgres.NewIndexRebuildLock(postgres.DirectSessionsOf(pool))
	skips := indexSkipCounts{}
	unavailable := errors.New("поиск временно недоступен")
	var attempts []string
	failed := false
	probe := indexRecoveryProbe{
		OfferIndex: index,
		before: func(docs []ports.OfferDocument) error {
			attempts = append(attempts, docs[0].ProductID)
			if docs[0].ProductID == "p11" && !failed {
				failed = true
				return unavailable
			}
			return nil
		},
	}
	makeIndexer := func() *appcatalog.Indexer {
		indexer := appcatalog.NewIndexer(postgres.NewUnitOfWork(pool), probe, clock, 10, lock, nil, time.Minute)
		indexer.SetSkipObserver(skips)
		return indexer
	}
	first, err := makeIndexer().Rebuild(ctx)
	if !errors.Is(err, unavailable) || first.Indexed != 9 || first.Skipped != 1 {
		t.Fatalf("первая страница не подтверждена: %+v, %v", first, err)
	}
	attempts = nil
	replica := makeIndexer()
	result, err := replica.Rebuild(ctx)
	if err != nil || result.Generation != first.Generation || result.Indexed != 19 || result.Skipped != 1 ||
		skips["rebuild"] != 1 || !slices.Equal(attempts, []string{"p11"}) {
		t.Fatalf("повтор пересборки: %+v, вызовы %v, метрика %v, ошибка %v", result, attempts, skips, err)
	}
	page, err := index.Search(ctx, ports.OfferQuery{Limit: 20})
	if err != nil || page.Total != 19 {
		t.Fatalf("выдача после пересборки: %d, %v", page.Total, err)
	}

	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().Enqueue(ctx, appcatalog.SyncTenantTask("t1", clock.Now()))
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher := messaging.NewDispatcher(
		postgres.NewTaskQueue(postgres.Single(pool)),
		clock,
		messaging.DispatcherConfig{
			Kinds:        []string{appcatalog.TaskSyncTenant},
			OrderedKinds: []string{appcatalog.TaskSyncTenant},
			Batch:        10,
			PerTenant:    10,
			Lease:        time.Minute,
		},
		map[string]messaging.Handler{appcatalog.TaskSyncTenant: replica.HandleTask},
	)
	attempts = nil
	runTenantIndexPage(t, dispatcher, clock)
	if skips["tenant"] != 1 {
		t.Fatalf("нет пропуска отклонённого предложения: %v", skips)
	}
	registry := appparticipant.NewRegistry(uow, clock, identity)
	statuses := []participant.TenantStatus{
		participant.TenantSuspended,
		participant.TenantActive,
		participant.TenantSuspended,
		participant.TenantActive,
		participant.TenantSuspended,
	}
	for n, status := range statuses {
		clock.Advance(time.Second)
		event := appparticipant.RegistryEvent{
			EventID: fmt.Sprintf("registry:%d", n),
			Event: participant.RegistryEvent{
				TenantID: "t1",
				Slug:     "t1",
				Status:   status,
				At:       clock.Now(),
			},
		}
		if err := registry.Apply(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	attempts = nil
	for range 5 {
		runTenantIndexPage(t, dispatcher, clock)
	}
	if want := []string{"p11", "p01", "p11"}; !slices.Equal(attempts, want) {
		t.Fatalf("индексация магазина: вызовы %v, ожидалось %v", attempts, want)
	}
	outcomes, err := dispatcher.RunOnce(ctx)
	if err != nil || len(outcomes) != 0 {
		t.Fatalf("лишние продолжения: %v, %v", outcomes, err)
	}
	page, err = index.Search(ctx, ports.OfferQuery{Limit: 20})
	if err != nil || page.Total != 0 {
		t.Fatalf("отключённый магазин в выдаче: %d, %v", page.Total, err)
	}
}

func runTenantIndexPage(t *testing.T, dispatcher *messaging.Dispatcher, clock *fake.Clock) {
	t.Helper()
	for range 10 {
		outcomes, err := dispatcher.RunOnce(context.Background())
		if err != nil || len(outcomes) != 1 {
			t.Fatalf("страница магазина: %+v, %v", outcomes, err)
		}
		if outcomes[0].Err == nil {
			return
		}
		if !errors.Is(outcomes[0].Err, ports.ErrOfferCapacity) {
			t.Fatal(outcomes[0].Err)
		}
		// После активации поиск асинхронно получает статистику нового поколения.
		clock.Advance(time.Hour)
	}
	t.Fatal("поиск не подтвердил запись после расчёта ёмкости")
}

func seedIndexRecoveryCatalog(t *testing.T, uow ports.UnitOfWork, clock ports.Clock, identity *fake.IDs) {
	t.Helper()
	ctx := context.Background()
	_, err := apptaxonomy.NewService(uow, clock, nil).Apply(ctx, taxonomy.Release{
		Checksum: strings.Repeat("a", 64),
		Version:  1,
		Attributes: []taxonomy.Attribute{
			{Code: "a1", Name: "Атрибут", Type: "string"},
		},
		Schemas: map[string]taxonomy.Schema{"s1": {Attributes: []string{"a1"}}},
		Categories: []taxonomy.Category{
			{ID: "c1", Slug: "c1", Name: "Категория", Schema: "s1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		event := participant.RegistryEvent{
			TenantID: "t1",
			Slug:     "t1",
			Status:   participant.TenantActive,
			At:       clock.Now(),
		}
		tenant, err := participant.NewTenant(event, clock.Now())
		if err != nil {
			return err
		}
		seller, err := participant.New(tenant, "Магазин", clock.Now())
		if err != nil {
			return err
		}
		readiness := participant.CardReadiness{Ready: true}
		if _, err := seller.ApplyStoreCard(participant.Profile{}, readiness, nil, clock.Now()); err != nil {
			return err
		}
		if _, err := seller.ApplyShopSettings(w3PipelineIntegrationDelivery, clock.Now()); err != nil {
			return err
		}
		if _, err := seller.SetTrust(participant.Trusted, clock.Now()); err != nil {
			return err
		}
		if _, err := seller.Enable(clock.Now()); err != nil {
			return err
		}
		if err := tx.Registry().Save(ctx, tenant); err != nil {
			return err
		}
		return tx.Participants().Add(ctx, seller)
	})
	if err != nil {
		t.Fatal(err)
	}
	moderation := appmoderation.NewService(uow, clock, identity, appmoderation.Config{ProbationOffers: 50})
	projector := appcatalog.NewProjector(uow, clock, moderation)
	events := make([]appcatalog.ProductEvent, 0, 20)
	for n := 1; n <= 20; n++ {
		product := ids.ProductID(fmt.Sprintf("p%02d", n))
		events = append(events, appcatalog.ProductEvent{Snapshot: recoverySnapshot(product, clock.Now())})
	}
	if err := projector.ApplyProducts(ctx, events); err != nil {
		t.Fatal(err)
	}
}

func recoverySnapshot(product ids.ProductID, at time.Time) catalog.Snapshot {
	return catalog.Snapshot{
		TenantID:  "t1",
		ProductID: product,
		Revision:  1,
		At:        at,
		Available: 2,
		HasPrice:  true,
		Price:     money.FromKopecks(100),
		BasePrice: money.FromKopecks(100),
		Card: catalog.Card{
			Name:    "Товар",
			Visible: true,
			Marketplace: &taxonomy.Card{
				CategoryID: "c1",
				Published:  true,
				Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`"Значение"`)},
			},
		},
	}
}
