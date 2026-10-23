//go:build integration

package kafka_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	kafkago "github.com/segmentio/kafka-go"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	infrakafka "github.com/proshop-app/proshop-marketplace/internal/infrastructure/kafka"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

// Сообщения сервера в том виде, в каком их пишет продюсер; время события
// подставляется в tenantCreatedPayload при отправке.
const (
	tenantCreatedPayload = `{
		"tenantId": "t1",
		"slug": "shop-one",
		"status": "active",
		"cell": "a",
		"plan": null,
		"hosts": [],
		"at": "%s"
	}`

	productProjectedPayload = `{
		"version": 1,
		"tenantId": "t1",
		"productId": "p1",
		"revision": 1,
		"operation": "upsert",
		"product": {
			"id": "p1",
			"name": "Чайник",
			"price": 1990.5,
			"promoPrice": null,
			"availableQuantity": 4,
			"categories": [],
			"tags": ["tag-1"],
			"conditions": {"isVisible": true}
		}
	}`

	tagProjectedPayload = `{
		"version": 1,
		"tenantId": "t1",
		"tagId": "tag-1",
		"revision": 1,
		"operation": "upsert",
		"tag": {"id": "tag-1", "value": "Новинка"}
	}`
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type pipeline struct {
	pool     *pgxpool.Pool
	consumer *kafka.Consumer
	reader   *infrakafka.Reader
}

func brokers() []string {
	if list := os.Getenv("TEST_KAFKA_BROKERS"); list != "" {
		return strings.Split(list, ",")
	}

	return []string{"localhost:29094"}
}

func createTopics(t *testing.T, topics ...string) {
	t.Helper()

	conn, err := kafkago.Dial("tcp", brokers()[0])
	if err != nil {
		t.Fatalf("брокер для тестов: %v", err)
	}
	defer func() { _ = conn.Close() }()

	controller, err := conn.Controller()
	if err != nil {
		t.Fatal(err)
	}

	admin, err := kafkago.Dial("tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()

	configs := make([]kafkago.TopicConfig, 0, len(topics))
	for _, topic := range topics {
		configs = append(configs, kafkago.TopicConfig{Topic: topic, NumPartitions: 3, ReplicationFactor: 1})
	}

	if err := admin.CreateTopics(configs...); err != nil {
		t.Fatalf("создание топиков: %v", err)
	}

	t.Cleanup(func() { deleteTopics(t, topics...) })

	deadline := time.Now().Add(30 * time.Second)
	for {
		partitions, err := conn.ReadPartitions(topics...)
		if err == nil && len(partitions) == 3*len(topics) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("топики не появились в метаданных: %d партиций, %v", len(partitions), err)
		}

		time.Sleep(200 * time.Millisecond)
	}
}

func deleteTopics(t *testing.T, topics ...string) {
	t.Helper()

	client := &kafkago.Client{Addr: kafkago.TCP(brokers()...), Timeout: 10 * time.Second}
	if _, err := client.DeleteTopics(context.Background(), &kafkago.DeleteTopicsRequest{Topics: topics}); err != nil {
		t.Errorf("удаление топиков: %v", err)
	}
}

func topicExists(t *testing.T, topic string) bool {
	t.Helper()

	conn, err := kafkago.Dial("tcp", brokers()[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	partitions, err := conn.ReadPartitions()
	if err != nil {
		t.Fatal(err)
	}

	for _, partition := range partitions {
		if partition.Topic == topic {
			return true
		}
	}

	return false
}

func produce(t *testing.T, messages ...kafkago.Message) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)

	for {
		// свежий писатель на каждую попытку: метаданные нового топика доходят не сразу
		writer := &kafkago.Writer{
			Addr:         kafkago.TCP(brokers()...),
			Balancer:     &kafkago.Hash{},
			RequiredAcks: kafkago.RequireAll,
		}
		err := writer.WriteMessages(context.Background(), messages...)
		_ = writer.Close()

		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("публикация: %v", err)
		}

		time.Sleep(500 * time.Millisecond)
	}
}

func unique(name string) string {
	return fmt.Sprintf("%s%d", name, time.Now().UnixNano())
}

func TestReaderCommitsOnlyProcessed(t *testing.T) {
	topic := unique("reader-test-")
	group := unique("group-")
	createTopics(t, topic)
	produce(
		t,
		kafkago.Message{Topic: topic, Key: []byte("k"), Value: []byte("first")},
		kafkago.Message{Topic: topic, Key: []byte("k"), Value: []byte("second")},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := infrakafka.Config{
		Brokers: brokers(),
		GroupID: group,
		Topics:  []string{topic},
		Logger:  quiet,
	}

	reader := infrakafka.NewReader(cfg)

	first, err := reader.Fetch(ctx)
	if err != nil || string(first.Value) != "first" {
		t.Fatalf("первое сообщение: %q, %v", first.Value, err)
	}
	if err := reader.Commit(ctx, first); err != nil {
		t.Fatal(err)
	}

	second, err := reader.Fetch(ctx)
	if err != nil || string(second.Value) != "second" {
		t.Fatalf("второе сообщение: %q, %v", second.Value, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	again := infrakafka.NewReader(cfg)
	defer func() { _ = again.Close() }()

	redelivered, err := again.Fetch(ctx)
	if err != nil || string(redelivered.Value) != "second" {
		t.Fatalf("незафиксированное сообщение приходит снова, зафиксированное — нет: %q, %v", redelivered.Value, err)
	}
}

func TestReaderSubscribesOnlyExistingTopics(t *testing.T) {
	present := unique("present-")
	absent := unique("absent-")
	createTopics(t, present)
	produce(t, kafkago.Message{Topic: present, Value: []byte("из существующего")})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	reader := infrakafka.NewReader(infrakafka.Config{
		Brokers:         brokers(),
		GroupID:         unique("group-"),
		Topics:          []string{present, absent},
		RefreshInterval: time.Second,
		Logger:          quiet,
	})
	defer func() { _ = reader.Close() }()

	first, err := reader.Fetch(ctx)
	if err != nil || string(first.Value) != "из существующего" {
		t.Fatalf("существующий топик читается без недостающего: %q, %v", first.Value, err)
	}
	if err := reader.Commit(ctx, first); err != nil {
		t.Fatal(err)
	}

	time.Sleep(2 * time.Second)
	if topicExists(t, absent) {
		t.Fatal("читатель не заводит у брокера чужой топик")
	}

	createTopics(t, absent)
	produce(t, kafkago.Message{Topic: absent, Value: []byte("из появившегося")})

	next, err := reader.Fetch(ctx)
	if err != nil || string(next.Value) != "из появившегося" {
		t.Fatalf("появившийся топик подхватывается без перезапуска: %q, %v", next.Value, err)
	}
	if err := reader.Commit(ctx, next); err != nil {
		t.Fatal(err)
	}
	if subscribed := reader.Subscribed(); len(subscribed) != 2 {
		t.Fatalf("подписка расширилась: %v", subscribed)
	}
}

func newPipeline(t *testing.T, pool *pgxpool.Pool, prefix, group string) *pipeline {
	t.Helper()

	uow := postgres.NewUnitOfWork(pool)
	router := kafka.NewRouter(
		appcatalog.NewProjector(uow, clock.System{}),
		appparticipant.NewRegistry(uow, clock.System{}),
	)
	reader := infrakafka.NewReader(infrakafka.Config{
		Brokers: brokers(),
		GroupID: group,
		Topics:  kafka.FullTopics(prefix, router),
		Logger:  quiet,
	})

	cfg := kafka.ConsumerConfig{
		TopicPrefix: prefix,
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return 10 * time.Millisecond },
		Ready:       pool.Ping,
		Now:         clock.System{}.Now,
	}
	consumer := kafka.NewConsumer(reader, router, messaging.NewParker(uow), cfg, quiet, prometheus.NewRegistry())

	return &pipeline{pool: pool, consumer: consumer, reader: reader}
}

func (p *pipeline) run(t *testing.T) context.CancelFunc {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		if err := p.consumer.Run(ctx); err != nil {
			t.Errorf("консьюмер: %v", err)
		}
	}()

	return func() {
		cancel()
		<-done
		_ = p.reader.Close()
	}
}

func waitFor(t *testing.T, pool *pgxpool.Pool, what string, query string, want int) {
	t.Helper()

	deadline := time.Now().Add(60 * time.Second)

	for {
		var got int
		if err := pool.QueryRow(context.Background(), query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d, ожидалось %d", what, got, want)
		}

		time.Sleep(100 * time.Millisecond)
	}
}

func count(t *testing.T, pool *pgxpool.Pool, query string) int {
	t.Helper()

	var n int
	if err := pool.QueryRow(context.Background(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}

	return n
}

func TestConsumerEndToEnd(t *testing.T) {
	pool := pgtest.Pool(t)
	prefix := unique("shop")
	group := unique("marketplace-")

	topics := kafka.FullTopics(prefix, kafka.NewRouter(nil, nil))
	createTopics(t, topics...)

	topic := func(name string) string { return prefix + "." + name }
	header := func(tenant string) []kafkago.Header {
		return []kafkago.Header{{Key: kafka.HeaderTenant, Value: []byte(tenant)}}
	}
	now := time.Now().UTC()

	produce(
		t,
		kafkago.Message{
			Topic: topic(kafka.TopicTenantCreated),
			Key:   []byte("t1"),
			Time:  now,
			Value: []byte(fmt.Sprintf(tenantCreatedPayload, now.Format(time.RFC3339Nano))),
		},
		kafkago.Message{
			Topic: topic(kafka.TopicProductProjected),
			Key:   []byte(`["t1","p1"]`),
			Time:  now,
			Value: []byte(productProjectedPayload),
		},
		kafkago.Message{
			Topic: topic(kafka.TopicTagProjected),
			Key:   []byte(`["t1","tag-1"]`),
			Time:  now,
			Value: []byte(tagProjectedPayload),
		},
		kafkago.Message{
			Topic:   topic(kafka.TopicStockItemUpdated),
			Key:     []byte("s1"),
			Time:    now.Add(time.Second),
			Headers: header("t1"),
			Value:   []byte(`{"id":"s1","productId":"p1","quantity":5,"reserved":1,"price":1990.5}`),
		},
		kafkago.Message{
			Topic:   topic(kafka.TopicProductPriceUpdated),
			Key:     []byte("p1"),
			Time:    now.Add(2 * time.Second),
			Headers: header("t1"),
			Value:   []byte(`{"id":"p1","price":2100}`),
		},
		kafkago.Message{
			Topic: topic(kafka.TopicProductPriceUpdated),
			Key:   []byte("p2"),
			Time:  now,
			Value: []byte(`{"id":"p2","price":100}`),
		},
		kafkago.Message{
			Topic:   topic(kafka.TopicStockItemUpdated),
			Key:     []byte("s2"),
			Time:    now,
			Headers: header("t1"),
			Value:   []byte(`{не json`),
		},
	)

	first := newPipeline(t, pool, prefix, group)
	stop := first.run(t)

	waitFor(t, pool, "отравленные", `SELECT count(*) FROM parked_events`, 2)
	waitFor(t, pool, "обработанные события", `SELECT count(*) FROM processed_events`, 5)
	stop()

	parkedWithoutTenant := `
		SELECT count(*)
		FROM parked_events
		WHERE reason = 'kafka.tenant_missing' AND tenant_id IS NULL`
	if n := count(t, pool, parkedWithoutTenant); n != 1 {
		t.Fatalf("сообщение без магазина — в отравленных без магазина: %d", n)
	}

	parkedWithTenant := `
		SELECT count(*)
		FROM parked_events
		WHERE reason = 'kafka.invalid_payload' AND tenant_id = 't1'`
	if n := count(t, pool, parkedWithTenant); n != 1 {
		t.Fatalf("повреждённое сообщение — в отравленных с магазином: %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM tenants WHERE tenant_id = 't1' AND status = 'active'`); n != 1 {
		t.Fatal("зеркало реестра заполнено")
	}

	projected := `
		SELECT count(*)
		FROM catalog_products
		WHERE tenant_id = 't1' AND product_id = 'p1' AND price = 199050 AND available = 4`
	if n := count(t, pool, projected); n != 1 {
		t.Fatal("снимок товара в копейках")
	}

	signals := `
		SELECT count(*)
		FROM offers
		WHERE tenant_id = 't1' AND product_id = 'p1' AND available = 4 AND price = 210000`
	if n := count(t, pool, signals); n != 1 {
		t.Fatal("сигналы остатка и цены: свежий прайс побеждает")
	}
	if n := count(t, pool, `SELECT count(*) FROM catalog_tags WHERE value = 'Новинка'`); n != 1 {
		t.Fatal("тег в проекции")
	}
	if n := count(t, pool, `SELECT count(*) FROM outbox WHERE kind = 'catalog.offer.sync'`); n < 1 {
		t.Fatal("изменения проекции ставят задачу синхронизации индекса")
	}

	processed := count(t, pool, `SELECT count(*) FROM processed_events`)

	second := newPipeline(t, pool, prefix, group)
	stopSecond := second.run(t)
	time.Sleep(3 * time.Second)
	stopSecond()

	if n := count(t, pool, `SELECT count(*) FROM processed_events`); n != processed {
		t.Fatalf("после коммита та же группа не перечитывает сообщения: %d, было %d", n, processed)
	}
	if n := count(t, pool, `SELECT count(*) FROM parked_events`); n != 2 {
		t.Fatalf("отравленные не дублируются: %d", n)
	}
}

func TestConsumerDeduplicatesRedelivery(t *testing.T) {
	pool := pgtest.Pool(t)
	p := newPipeline(t, pool, "shop", unique("dedup-"))
	defer func() { _ = p.reader.Close() }()

	message := kafkago.Message{
		Topic:     "shop." + kafka.TopicProductPriceUpdated,
		Partition: 1,
		Offset:    42,
		Key:       []byte("p1"),
		Time:      time.Now(),
		Headers: []kafkago.Header{
			{Key: kafka.HeaderTenant, Value: []byte("t1")},
		},
		Value: []byte(`{"id":"p1","price":10}`),
	}

	for range 2 {
		if err := p.consumer.Process(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}

	if n := count(t, pool, `SELECT version FROM offers WHERE tenant_id = 't1' AND product_id = 'p1'`); n != 1 {
		t.Fatalf("повторная доставка той же позиции не применяется дважды: версия %d", n)
	}

	processed := `
		SELECT count(*)
		FROM processed_events
		WHERE id = 'shop.catalog.product.price-updated:1:42'`
	if n := count(t, pool, processed); n != 1 {
		t.Fatal("отметка события по позиции в топике")
	}
}
