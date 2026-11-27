//go:build integration

package searchapi_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	kafkago "github.com/segmentio/kafka-go"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

const stockSummarySnapshot = `{
	"version": 1, "tenantId": "t1", "productId": "%s", "revision": %d, "operation": "upsert",
	"product": {
		"id": "%s", "name": "Товар", "availableQuantity": %d,
		"priceProfiles": [{"key": "marketplace:default", "price": 100, "effectivePrice": 100}],
		"conditions": {"isVisible": true},
		"marketplace": {"categoryId": "c1", "published": true, "attributes": {"a1": "value"}}
	}
}`

func TestStockSnapshotUpdatesIndexWithoutStoreSummaryTask(t *testing.T) {
	index := testSearchIndex(t)
	pool := pgtest.Pool(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	generation, err := index.NewGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Activate(ctx, generation); err != nil {
		t.Fatal(err)
	}
	uow := postgres.NewUnitOfWork(pool)
	clock := fake.NewClock(time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC))
	seedStockSummarySeller(t, uow, clock)
	member := appparticipant.NewService(uow, clock, &fake.IDs{})
	indexer := appcatalog.NewIndexer(uow, index, clock, 100, nil, nil, time.Second)
	statuses := moderationstatus.NewService(uow, clock, &fake.Publisher{}, "offer", "participant")
	dispatcher := messaging.NewDispatcher(
		postgres.NewTaskQueue(postgres.Single(pool)),
		clock,
		messaging.DispatcherConfig{Batch: 10, PerTenant: 10, Lease: time.Minute},
		map[string]messaging.Handler{
			appcatalog.TaskSyncOffer:   indexer.HandleTask,
			ports.TaskStoreCard:        member.RefreshStoreCard,
			moderationstatus.TaskOffer: statuses.HandleTask,
		},
	)
	dispatcher.RegisterBatch(appcatalog.TaskSyncOffer, indexer.HandleOfferBatch)
	moderator := appmoderation.NewService(uow, clock, &fake.IDs{}, appmoderation.Config{})
	router := kafka.NewRouter(appcatalog.NewProjector(uow, clock, moderator), nil)
	consumer := kafka.NewConsumer(
		nil,
		router,
		messaging.NewParker(uow),
		kafka.ConsumerConfig{TopicPrefix: "shop", Now: clock.Now, MaxAttempts: 1, Ready: pool.Ping},
		slog.Default(),
		kafka.NewConsumerMetrics(prometheus.NewRegistry()),
	)
	drain := func() int {
		t.Helper()
		refreshes := 0
		for {
			clock.Advance(time.Second)
			outcomes, err := dispatcher.RunOnce(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(outcomes) == 0 {
				return refreshes
			}
			for _, outcome := range outcomes {
				if outcome.Err != nil {
					t.Fatal(outcome.Err)
				}
				if outcome.Task.Kind == ports.TaskStoreCard {
					refreshes++
				}
			}
		}
	}
	project := func(id string, rev, available int) {
		t.Helper()
		msg := kafkago.Message{
			Topic:  "shop." + kafka.TopicProductProjected,
			Key:    []byte(fmt.Sprintf(`["t1","%s"]`, id)),
			Value:  []byte(fmt.Sprintf(stockSummarySnapshot, id, rev, id, available)),
			Time:   clock.Now(),
			Offset: clock.Now().Unix(),
		}
		if err := consumer.Process(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	project("p1", 1, 4)
	clock.Advance(time.Second)
	project("p2", 1, 4)
	if refreshes := drain(); refreshes != 1 {
		t.Fatalf("первичные карточки должны поставить одну сводку: %d", refreshes)
	}

	stock := kafkago.Message{
		Topic: "shop." + kafka.TopicStockItemUpdated,
		Key:   []byte("s1"),
		Headers: []kafkago.Header{
			{Key: kafka.HeaderTenant, Value: []byte("t1")},
		},
		Value: []byte(`{"id":"s1","productId":"p1","quantity":9,"reserved":1,"price":100}`),
		Time:  clock.Now(),
	}
	if err := consumer.Process(ctx, stock); err != nil {
		t.Fatal(err)
	}
	if refreshes := drain(); refreshes != 0 {
		t.Fatalf("складское событие поставило сводку: %d", refreshes)
	}
	fromStock, found, err := index.Get(ctx, "t1~p1")
	if err != nil || !found || fromStock.Available != 8 {
		t.Fatalf("остаток после stock.item.updated: %+v, %v, %v", fromStock, found, err)
	}

	project("p1", 2, 8)
	if refreshes := drain(); refreshes != 0 {
		t.Fatalf("снимок склада поставил сводку: %d", refreshes)
	}
	fromSnapshot, found, err := index.Get(ctx, "t1~p1")
	if err != nil || !found || fromSnapshot.Available != 8 || fromSnapshot.Version <= fromStock.Version {
		t.Fatalf("снимок склада не дошёл до поиска: %+v, %v, %v", fromSnapshot, found, err)
	}
	summary, err := member.StoreSummary(ctx, "t1")
	if err != nil || len(summary.Products) != 2 || summary.Products[0].ProductID != "p2" {
		t.Fatalf("складское событие поменяло витрину: %+v, %v", summary, err)
	}
}

func seedStockSummarySeller(t *testing.T, uow ports.UnitOfWork, clock ports.Clock) {
	t.Helper()
	ctx := context.Background()
	_, err := apptaxonomy.NewService(uow, clock, nil).Apply(ctx, taxonomy.Release{
		Checksum: strings.Repeat("a", 64),
		Version:  1,
		Attributes: []taxonomy.Attribute{
			{Code: "a1", Name: "Атрибут", Type: "string"},
		},
		Schemas: map[string]taxonomy.Schema{
			"s1": {Attributes: []string{"a1"}},
		},
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
			Slug:     "shop-one",
			Status:   participant.TenantActive,
			At:       clock.Now(),
		}
		tenant, err := participant.NewTenant(event, clock.Now())
		if err != nil {
			return err
		}
		if err := tx.Registry().Save(ctx, tenant); err != nil {
			return err
		}
		p, err := participant.New(tenant, "Магазин", clock.Now())
		if err != nil {
			return err
		}
		if _, err := p.SetTrust(participant.Trusted, clock.Now()); err != nil {
			return err
		}
		if _, err := p.ApplyShopSettings(w3PipelineIntegrationDelivery, clock.Now()); err != nil {
			return err
		}
		profile := participant.Profile{}
		readiness := participant.CardReadiness{Ready: true}
		if _, err := p.ApplyStoreCard(profile, readiness, nil, clock.Now()); err != nil {
			return err
		}
		if _, err := p.Enable(clock.Now()); err != nil {
			return err
		}
		return tx.Participants().Add(ctx, p)
	})
	if err != nil {
		t.Fatal(err)
	}
}
