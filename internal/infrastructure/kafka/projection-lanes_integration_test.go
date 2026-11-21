//go:build integration

package kafka_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	kafkago "github.com/segmentio/kafka-go"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	infrakafka "github.com/proshop-app/proshop-marketplace/internal/infrastructure/kafka"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const laneSnapshotPayload = `{
	"version":1,"tenantId":"t0","productId":%q,"revision":%d,"operation":"upsert",
	"product":{
		"id":%q,"name":"Товар",
		"priceProfiles":[{"key":"marketplace:default","price":%d,"effectivePrice":%d}],
		"variantsId":"v1",
		"variants":{"groupId":"v1","position":0,
			"axes":[{"key":"platform.a1","code":"a1","name":"Атрибут","value":%q}]},
		"conditions":{"isVisible":%t,"isOnlyVariant":%t},
		"marketplace":{"categoryId":"c1","published":%t,"attributes":{}}
	}
}`

type laneSnapshot struct {
	revision    int64
	deleted     bool
	published   bool
	onlyVariant bool
	variant     string
}

func TestProjectionLaneRevisionOrder(t *testing.T) {
	pool, _, uow := intakeDatabase(t, measurePool)
	prefix, group := unique("lane"), unique("marketplace-")
	moderation := appmoderation.NewService(uow, clock.System{}, random.IDs{}, appmoderation.Config{})
	router := kafka.NewRouter(appcatalog.NewProjector(uow, clock.System{}, moderation), nil)
	regular, bulk := kafka.SubscriptionTopics(prefix, router)
	point := prefix + "." + kafka.TopicProductProjected
	createTopics(t, point, bulk)

	cfg := kafka.ConsumerConfig{
		TopicPrefix: prefix,
		MaxAttempts: 3,
		Ready:       pool.Ping,
		Backoff:     messaging.Backoff,
		Now:         time.Now,
	}
	metrics := kafka.NewConsumerMetrics(prometheus.NewRegistry())
	start := func(id string, topics []string) {
		reader := infrakafka.NewReader(infrakafka.Config{
			Brokers: brokers(),
			GroupID: id,
			Topics:  topics,
			Logger:  quiet,
		})
		consumer := kafka.NewConsumer(reader, router, messaging.NewParker(uow), cfg, quiet, metrics)
		p := &pipeline{pool: pool, consumer: consumer, reader: reader}
		t.Cleanup(p.run(t))
	}
	start(group, regular)
	start(group+"-bulk", []string{bulk})

	const processedQuery = `SELECT count(*) FROM processed_events`
	processed := count(t, pool, processedQuery)
	for i, newer := range []string{point, bulk} {
		older := bulk
		if newer == bulk {
			older = point
		}
		t.Run([]string{"основная-новее", "массовая-новее"}[i], func(t *testing.T) {
			product := fmt.Sprintf("p%d", i+1)
			// Позднему снимку даём и более позднее время Kafka, чтобы решала ревизия.
			publish := func(topic, product string, snapshot laneSnapshot) catalog.ProductState {
				t.Helper()
				produce(t, snapshot.message(topic, product))
				processed++
				waitFor(t, pool, "обработанные события", processedQuery, processed)
				return readLaneProduct(t, uow, product)
			}
			steps := []struct {
				fresh laneSnapshot
				stale laneSnapshot
			}{
				{
					fresh: laneSnapshot{revision: 2, published: true, variant: "v2"},
					stale: laneSnapshot{revision: 1, deleted: true},
				},
				{
					fresh: laneSnapshot{revision: 4, published: true, onlyVariant: true, variant: "v4"},
					stale: laneSnapshot{revision: 3, variant: "v3"},
				},
				{
					fresh: laneSnapshot{revision: 6, onlyVariant: true, variant: "v6"},
					stale: laneSnapshot{revision: 5, published: true, variant: "v5"},
				},
				{
					fresh: laneSnapshot{revision: 8, deleted: true},
					stale: laneSnapshot{revision: 7, published: true, variant: "v7"},
				},
			}
			for _, step := range steps {
				before := publish(newer, product, step.fresh)
				step.fresh.check(t, before)
				after := publish(older, product, step.stale)
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("старая ревизия %d изменила запись: %+v", step.stale.revision, after)
				}
			}

			product += "-deleted"
			deletion := laneSnapshot{revision: 2, deleted: true}
			before := publish(newer, product, deletion)
			deletion.check(t, before)
			after := publish(older, product, laneSnapshot{revision: 1, published: true, variant: "v1"})
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("старый снимок восстановил удалённый товар: %+v", after)
			}
		})
	}
	if parked := count(t, pool, countIntakeParked); parked != 0 {
		t.Fatalf("корректные проекции попали в парковку: %d", parked)
	}
}

func (s laneSnapshot) message(topic, product string) kafkago.Message {
	value := fmt.Sprintf(
		laneSnapshotPayload,
		product,
		s.revision,
		product,
		s.revision,
		s.revision,
		s.variant,
		s.published,
		s.onlyVariant,
		s.published,
	)
	if s.deleted {
		const deletion = `{"version":1,"tenantId":"t0","productId":%q,"revision":%d,"operation":"delete"}`
		value = fmt.Sprintf(deletion, product, s.revision)
	}
	return kafkago.Message{
		Topic:   topic,
		Key:     []byte(fmt.Sprintf(`["t0",%q]`, product)),
		Value:   []byte(value),
		Time:    time.Now(),
		Headers: []kafkago.Header{{Key: kafka.HeaderTenant, Value: []byte("t0")}},
	}
}

func (s laneSnapshot) check(t *testing.T, got catalog.ProductState) {
	t.Helper()
	if got.Revision != s.revision || got.Deleted != s.deleted || got.Card.Publication.Published != s.published {
		t.Fatalf("состояние ревизии %d: %+v", s.revision, got)
	}
	if s.deleted {
		if got.HasPrice || got.Price.Kopecks() != 0 || got.BasePrice.Kopecks() != 0 {
			t.Fatalf("цены удалённого товара: %+v", got)
		}
		return
	}
	if !got.HasPrice || got.Price.Kopecks() != s.revision*100 || got.BasePrice.Kopecks() != s.revision*100 {
		t.Fatalf("публичная цена ревизии %d: %+v", s.revision, got)
	}
	variants := got.Card.Variants
	if got.Card.Visible != s.published || got.Card.OnlyVariant != s.onlyVariant ||
		variants == nil || len(variants.Axes) != 1 || variants.Axes[0].Value != s.variant {
		t.Fatalf("карточка ревизии %d: %+v", s.revision, got.Card)
	}
}

func readLaneProduct(t *testing.T, uow ports.UnitOfWork, product string) catalog.ProductState {
	t.Helper()
	var state catalog.ProductState
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		offer, err := tx.Products().Get(ctx, "t0", ids.ProductID(product))
		if err == nil {
			state = offer.State()
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}
