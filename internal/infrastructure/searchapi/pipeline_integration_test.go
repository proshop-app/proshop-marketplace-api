//go:build integration

package searchapi_test

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// TestProjectionToIndex — проекция в PostgreSQL, задачи outbox, индекс в
// OpenSearch и выдача: весь путь синхронизации на настоящих хранилищах.
var w3PipelineIntegrationDelivery = participant.DeliverySettings{
	Regions:  []string{"RU-MOW"},
	Carriers: []string{"cdek"},
}
var w3PipelineIntegrationSubsidy = participant.Subsidy{
	Enabled: true,
	Type:    participant.SubsidyFixed,
}

func TestProjectionToIndex(t *testing.T) {
	pool := pgtest.Pool(t)
	index := testSearchIndex(t)
	ctx := context.Background()
	now := clock.System{}
	uow := postgres.NewUnitOfWork(pool)

	if _, err := apptaxonomy.NewService(uow, now, nil).Apply(ctx, taxonomy.Release{
		Checksum: strings.Repeat("a", 64),
		Attributes: []taxonomy.Attribute{
			{
				Code:  "a1",
				Name:  "Атрибут 1",
				Type:  "string",
				Facet: true,
			},
			{Code: "a2", Name: "Атрибут 2", Type: "number", Facet: true},
			{Code: "a3", Name: "Атрибут 3", Type: "boolean", Facet: true},
		},
		Schemas: map[string]taxonomy.Schema{
			"s1": {Attributes: []string{"a1", "a2", "a3"}, Optional: []string{"a2", "a3"}},
		},
		Version: 1,
		Categories: []taxonomy.Category{
			{
				ID:     "c1",
				Slug:   "c1",
				Name:   "Категория",
				Schema: "s1",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	moderationService := appmoderation.NewService(uow, now, &fake.IDs{}, appmoderation.Config{ProbationOffers: 50})
	projector := appcatalog.NewProjector(uow, now, moderationService)
	indexer := appcatalog.NewIndexer(uow, index, now, 100, postgres.NewIndexRebuildLock(pool), nil, 30*time.Second)
	identities := &fake.IDs{}
	registry := appparticipant.NewRegistry(uow, now, identities)
	participants := appparticipant.NewService(uow, now, identities)
	platform := fake.NewPlatform()
	settings := appparticipant.NewSettings(uow, platform, now, identities)
	queries := appcatalog.NewQueries(uow, index, platform, nil, nil, fake.NewClock(time.Time{}))
	statuses := moderationstatus.NewService(uow, now, &fake.Publisher{}, "offer", "participant")
	dispatcher := messaging.NewDispatcher(
		postgres.NewTaskQueue(pool),
		now,
		messaging.DispatcherConfig{
			Batch:     50,
			PerTenant: 50,
			Lease:     time.Minute,
		},
		map[string]messaging.Handler{
			appmoderation.TaskReevaluate:     moderationService.HandleTask,
			appcatalog.TaskSyncOffer:         indexer.HandleTask,
			appcatalog.TaskSyncTenant:        indexer.HandleTask,
			ports.TaskStoreCard:              participants.RefreshStoreCard,
			moderationstatus.TaskOffer:       statuses.HandleTask,
			moderationstatus.TaskParticipant: statuses.HandleTask,
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

	}
	active := participant.RegistryEvent{
		TenantID: "t1",
		Slug:     "shop-one",
		Status:   participant.TenantActive,
		Hosts:    json.RawMessage(`[]`),
		At:       now.Now(),
	}
	err := registry.Apply(ctx, appparticipant.RegistryEvent{
		EventID: "e1",
		Event:   active,
	})

	if err != nil {
		t.Fatal(err)
	}

	assets := []string{"p/detail.jpg", "p/main.jpg", "p/back.jpg"}
	for i, product := range []ids.ProductID{"p1", "p2"} {
		err := projector.ApplyProduct(
			ctx,
			appcatalog.ProductEvent{
				EventID: "product-" + string(product),
				Snapshot: catalog.Snapshot{
					TenantID:  "t1",
					ProductID: product,
					Revision:  1,
					At:        now.Now(),
					Available: 3,
					Price:     money.FromKopecks(int64(10000 * (i + 1))),
					Card: catalog.Card{
						Marketplace: &taxonomy.Card{
							CategoryID: "c1",
							Published:  true,
							Attributes: map[string]json.RawMessage{
								"a1": json.RawMessage(`"001"`),
								"a2": json.RawMessage(`1.75`),
								"a3": json.RawMessage(`false`),
							},
						},
						Assets:  assets,
						Name:    "Товар " + string(product),
						Visible: true,
						Categories: []catalog.Category{
							{
								ID:   "local",
								Name: "Категория магазина",
							},
						},
					},
					PriceProfiles: []catalog.PriceProfile{
						{
							Key:            catalog.DefaultPriceProfile,
							Price:          money.FromKopecks(int64(10000 * (i + 1))),
							EffectivePrice: money.FromKopecks(int64(10000 * (i + 1))),
						},
						{
							Key:            "marketplace:group",
							Price:          money.FromKopecks(int64(8000 * i)),
							EffectivePrice: money.FromKopecks(int64(8000 * i)),
						},
					},
				},
			},
		)

		if err != nil {
			t.Fatal(err)
		}
	}

	if err := projector.ApplyProduct(ctx, appcatalog.ProductEvent{
		EventID: "product-invalid",
		Snapshot: catalog.Snapshot{
			TenantID:  "t1",
			ProductID: "invalid",
			Revision:  1,
			At:        now.Now(),
			Card: catalog.Card{
				Marketplace: &taxonomy.Card{
					CategoryID: "c1",
					Published:  true,
				},
				Name:    "Неполная карточка",
				Visible: true,
			},
			PriceProfiles: []catalog.PriceProfile{
				{
					Key: catalog.DefaultPriceProfile,
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	drain()

	if list, _ := queries.Search(ctx, ports.OfferQuery{}); list.Total != 0 {
		t.Fatalf("магазин без участия не попадает в выдачу: %+v", list)
	}
	if _, err := participants.Get(ctx, "t1"); err != nil {
		t.Fatal(err)
	}

	decision := participant.Decision{Note: "Проверено"}
	if _, err := participants.SetTrust(ctx, "t1", participant.Trusted, decision); err != nil {
		t.Fatal(err)
	}
	platform.SetSettings("t1", ports.ShopSettings{Card: participant.CardReadiness{Ready: true}})
	if _, err := settings.SyncOne(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := participants.Enable(ctx, "t1", participant.Decision{}); err != nil {
		t.Fatal(err)
	}
	drain()

	if list, _ := queries.Search(ctx, ports.OfferQuery{}); list.Total != 0 {
		t.Fatalf("магазин скрыт без доставки: %+v", list)
	}

	platform.SetSettings("t1", ports.ShopSettings{
		Profile:  participant.Profile{Name: "Первый магазин"},
		Card:     participant.CardReadiness{Ready: true},
		Subsidy:  w3PipelineIntegrationSubsidy,
		Delivery: w3PipelineIntegrationDelivery,
	})

	if changed, err := settings.SyncOne(ctx, "t1"); err != nil || !changed {
		t.Fatalf("настройки магазина: %v, %v", changed, err)
	}
	drain()

	list, err := queries.Search(ctx, ports.OfferQuery{
		Text: "товари",
		Sort: ports.SortPriceDesc,
	})

	if err != nil || list.Total != 2 || list.Items[0].Offer.ID != "t1~p2" || !list.Items[0].Offer.CheckoutAvailable {
		t.Fatalf("включённый участник в выдаче, с настройками доступен для оформления: %+v, %v", list, err)
	}

	for _, item := range list.Items {
		if !slices.Equal(item.Offer.Assets, assets) {
			t.Fatalf("поиск изменил галерею: %v, ожидалась %v", item.Offer.Assets, assets)
		}
	}
	doc, found, err := index.Get(ctx, "t1~p1")
	if err != nil || !found || !slices.Equal(doc.Assets, assets) {
		t.Fatalf("Get изменил галерею: %+v, найдено %v, ошибка %v", doc, found, err)
	}

	card, err := queries.Offer(
		ctx,
		ids.OfferID{
			Tenant:  "t1",
			Product: "p1",
		},
		"",
	)

	if err != nil || !card.Offer.CheckoutAvailable || card.Offer.SellerName != "Первый магазин" {
		t.Fatalf("карточка после настроек участия: %+v, %v", card, err)
	}

	if len(card.Offer.PriceProfiles) != 2 ||
		!reflect.DeepEqual(card.Offer.Attributes["a3"], json.RawMessage(`false`)) ||
		!reflect.DeepEqual(card.Offer.Delivery, w3PipelineIntegrationDelivery) ||
		len(card.Offer.Categories) != 1 || card.Offer.Categories[0].ID != "c1" {
		t.Fatalf("потеряны проверенные данные предложения: %+v", card)
	}
	buyer := ids.UUID("11111111-1111-4111-8111-111111111111")
	platform.SetBuyerProfile(buyer, "t1", "marketplace:group")
	personalized, err := queries.Search(ctx, ports.OfferQuery{
		BuyerID: buyer,
		Sort:    ports.SortPriceAsc,
		Limit:   1,
		Attributes: map[string]ports.AttributeFilter{
			"a2": {
				Min: "1.5",
				Max: "2",
			},
		},
	})

	if err != nil || personalized.Total != 2 || len(personalized.Items) != 1 ||
		personalized.Items[0].Offer.ID != "t1~p1" || personalized.Items[0].Offer.Price != 0 {
		t.Fatalf("цена покупателя и фильтр до пагинации: %+v, %v", personalized, err)
	}

	if _, err := queries.Offer(
		ctx,
		ids.OfferID{
			Tenant:  "t1",
			Product: "invalid",
		},
		"",
	); err == nil {
		t.Fatal("карточка без обязательного атрибута стала публичной")
	}
	suspended := participant.RegistryEvent{
		TenantID: "t1",
		Slug:     "shop-one",
		Status:   participant.TenantSuspended,
		Hosts:    json.RawMessage(`[]`),
		At:       now.Now(),
	}

	err = registry.Apply(ctx, appparticipant.RegistryEvent{
		EventID: "e2",
		Event:   suspended,
	})

	if err != nil {
		t.Fatal(err)
	}
	drain()

	if list, _ := queries.Search(ctx, ports.OfferQuery{}); list.Total != 0 {
		t.Fatalf("приостановленный магазин уходит из выдачи: %+v", list)
	}

	doc, found, err = index.Get(ctx, "t1~p1")
	if err != nil || found || len(doc.Assets) != 0 {
		t.Fatalf("скрытое предложение вернуло карточку или галерею: %+v, найдено %v, ошибка %v", doc, found, err)
	}

	result, err := indexer.Rebuild(ctx)

	if err != nil || result.Indexed != 3 {
		t.Fatalf("перестроение из проекции: %+v, %v", result, err)
	}

	if _, err := queries.Offer(
		ctx,
		ids.OfferID{
			Tenant:  "t1",
			Product: "p1",
		},
		"",
	); err == nil {
		t.Fatal("после перестроения скрытое предложение по-прежнему скрыто")
	}
}
