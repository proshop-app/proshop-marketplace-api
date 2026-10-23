//go:build integration

package opensearch_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// TestProjectionToIndex — проекция в PostgreSQL, задачи outbox, индекс в
// OpenSearch и выдача: весь путь синхронизации на настоящих хранилищах.
func TestProjectionToIndex(t *testing.T) {
	pool := pgtest.Pool(t)
	index := testIndex(t)
	ctx := context.Background()
	now := clock.System{}

	uow := postgres.NewUnitOfWork(pool)
	projector := appcatalog.NewProjector(uow, now)
	indexer := appcatalog.NewIndexer(uow, index, now, 100)
	registry := appparticipant.NewRegistry(uow, now)
	participants := appparticipant.NewService(uow, now)
	platform := fake.NewPlatform()
	settings := appparticipant.NewSettings(uow, platform, now)
	queries := appcatalog.NewQueries(uow, index)
	dispatcher := messaging.NewDispatcher(
		postgres.NewTaskQueue(pool),
		now,
		messaging.DispatcherConfig{Batch: 50, PerTenant: 50, Lease: time.Minute},
		map[string]messaging.Handler{
			appcatalog.TaskSyncOffer:  indexer.HandleTask,
			appcatalog.TaskSyncTenant: indexer.HandleTask,
		},
	)

	if built, err := indexer.EnsureIndex(ctx); err != nil || !built {
		t.Fatalf("первый запуск собирает индекс: %v, %v", built, err)
	}

	drain := func() {
		t.Helper()

		for {
			outcomes, err := dispatcher.RunOnce(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, outcome := range outcomes {
				if outcome.Err != nil {
					t.Fatalf("задача %s: %v", outcome.Task.Kind, outcome.Err)
				}
			}
			if len(outcomes) == 0 {
				break
			}
		}

		if err := index.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
	}

	active := participant.RegistryEvent{
		TenantID: "t1",
		Slug:     "shop-one",
		Status:   participant.TenantActive,
		Hosts:    json.RawMessage(`[]`),
		At:       now.Now(),
	}

	err := registry.Apply(ctx, appparticipant.RegistryEvent{EventID: "e1", Event: active})
	if err != nil {
		t.Fatal(err)
	}

	for i, product := range []ids.ProductID{"p1", "p2"} {
		err := projector.ApplyProduct(
			ctx,
			appcatalog.ProductEvent{EventID: "product-" + string(product), Snapshot: catalog.Snapshot{
				TenantID:  "t1",
				ProductID: product,
				Revision:  1,
				At:        now.Now(),
				Available: 3,
				Price:     money.FromKopecks(int64(10000 * (i + 1))),
				Card: catalog.Card{
					Name:       "Чайник " + string(product),
					Visible:    true,
					Categories: []catalog.Category{{ID: "c1", Name: "Кухня"}},
				},
			}},
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	drain()
	if list, _ := queries.Search(ctx, ports.OfferQuery{}); list.Total != 0 {
		t.Fatalf("магазин без участия не попадает в выдачу: %+v", list)
	}

	command := appparticipant.CreateCommand{TenantID: "t1", Name: "Первый магазин"}
	if _, err := participants.Create(ctx, command); err != nil {
		t.Fatal(err)
	}
	if _, err := participants.Enable(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	drain()

	if list, _ := queries.Search(ctx, ports.OfferQuery{}); list.Total != 0 {
		t.Fatalf("магазин не включил участие в настройках — не в выдаче: %+v", list)
	}

	platform.SetSettings("t1", ports.ShopSettings{Enabled: true})
	if changed, err := settings.SyncOne(ctx, "t1"); err != nil || !changed {
		t.Fatalf("настройки магазина: %v, %v", changed, err)
	}
	drain()

	list, err := queries.Search(ctx, ports.OfferQuery{Text: "чайники", Sort: ports.SortPriceDesc})
	if err != nil || list.Total != 2 || list.Items[0].Offer.ID != "t1~p2" || list.Items[0].Offer.CheckoutAvailable {
		t.Fatalf("включённый участник в выдаче, без кассы — без checkout: %+v, %v", list, err)
	}

	if _, err := participants.SetPaymentAccount(ctx, "t1", "506751"); err != nil {
		t.Fatal(err)
	}
	drain()

	card, err := queries.Offer(ctx, ids.OfferID{Tenant: "t1", Product: "p1"})
	if err != nil || !card.Offer.CheckoutAvailable || card.Offer.SellerName != "Первый магазин" {
		t.Fatalf("карточка после привязки кассы: %+v, %v", card, err)
	}

	suspended := participant.RegistryEvent{
		TenantID: "t1",
		Slug:     "shop-one",
		Status:   participant.TenantSuspended,
		Hosts:    json.RawMessage(`[]`),
		At:       now.Now(),
	}

	err = registry.Apply(ctx, appparticipant.RegistryEvent{EventID: "e2", Event: suspended})
	if err != nil {
		t.Fatal(err)
	}
	drain()

	if list, _ := queries.Search(ctx, ports.OfferQuery{}); list.Total != 0 {
		t.Fatalf("приостановленный магазин уходит из выдачи: %+v", list)
	}

	result, err := indexer.Rebuild(ctx)
	if err != nil || result.Indexed != 2 {
		t.Fatalf("перестроение из проекции: %+v, %v", result, err)
	}
	if _, err := queries.Offer(ctx, ids.OfferID{Tenant: "t1", Product: "p1"}); err == nil {
		t.Fatal("после перестроения скрытое предложение по-прежнему скрыто")
	}
}
