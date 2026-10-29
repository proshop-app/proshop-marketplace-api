package kafka_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	kafkago "github.com/segmentio/kafka-go"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

type source struct {
	mu       sync.Mutex
	messages []kafkago.Message
	commits  []int64
}

type parker struct {
	failures int
	events   []ports.ParkedEvent
}

type flaky struct {
	recorder
	failures int
}

func (s *source) Fetch(ctx context.Context) (kafkago.Message, error) {
	s.mu.Lock()
	if len(s.messages) > 0 {
		next := s.messages[0]
		s.messages = s.messages[1:]
		s.mu.Unlock()

		return next, nil
	}
	s.mu.Unlock()

	<-ctx.Done()

	return kafkago.Message{}, ctx.Err()
}

func (s *source) Commit(_ context.Context, message kafkago.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.commits = append(s.commits, message.Offset)

	return nil
}

func (p *parker) Park(_ context.Context, event ports.ParkedEvent) error {
	if p.failures > 0 {
		p.failures--

		return errors.New("база недоступна")
	}

	p.events = append(p.events, event)

	return nil
}

func (f *flaky) ApplyPrice(ctx context.Context, e appcatalog.PriceEvent) error {
	if f.failures != 0 {
		if f.failures > 0 {
			f.failures--
		}

		return errors.New("временный сбой")
	}

	return f.recorder.ApplyPrice(ctx, e)
}

func consumer(
	src kafka.Source,
	projections kafka.Projections,
	park kafka.Parker,
	ready error,
) (*kafka.Consumer, *prometheus.Registry) {
	registry := prometheus.NewRegistry()
	cfg := kafka.ConsumerConfig{
		TopicPrefix: "shop",
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return time.Millisecond },
		Ready:       func(context.Context) error { return ready },
		Now:         func() time.Time { return published },
	}
	router := kafka.NewRouter(projections, &recorder{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := kafka.NewConsumer(src, router, park, cfg, logger, registry)

	return c, registry
}

func priceMessage(offset int64, headers ...kafkago.Header) kafkago.Message {
	msg := message("p1", `{"id":"p1","price":10}`, headers...)
	msg.Topic = "shop." + kafka.TopicProductPriceUpdated
	msg.Partition = 2
	msg.Offset = offset

	return msg
}

func TestConsumerProcessesAndCommits(t *testing.T) {
	rec := &recorder{}
	src := &source{}
	c, registry := consumer(src, rec, &parker{}, nil)

	if err := c.Process(context.Background(), priceMessage(5, tenantHeader("t1"))); err != nil {
		t.Fatal(err)
	}

	if len(rec.prices) != 1 || rec.prices[0].EventID != "shop.catalog.product.price-updated:2:5" {
		t.Fatalf("идентификатор события — позиция в топике: %+v", rec.prices)
	}
	if len(src.commits) != 1 || src.commits[0] != 5 {
		t.Fatalf("коммит после записи: %v", src.commits)
	}
	if got := sum(t, registry, "marketplace_kafka_messages_total"); got != 1 {
		t.Fatalf("метрика обработанных: %v", got)
	}
}

func TestConsumerParksMessageWithoutTenant(t *testing.T) {
	src := &source{}
	park := &parker{}
	c, registry := consumer(src, &recorder{}, park, nil)

	if err := c.Process(context.Background(), priceMessage(6)); err != nil {
		t.Fatal(err)
	}

	if len(park.events) != 1 || park.events[0].Reason != "kafka.tenant_missing" || park.events[0].TenantID != "" ||
		park.events[0].Offset != 6 {
		t.Fatalf("отравленное сообщение: %+v", park.events)
	}
	if len(src.commits) != 1 {
		t.Fatalf("после парковки позиция фиксируется: %v", src.commits)
	}
	if got := sum(t, registry, "marketplace_kafka_parked_total"); got != 1 {
		t.Fatalf("метрика отравленных: %v", got)
	}
}

func TestConsumerRetriesTransientFailure(t *testing.T) {
	src := &source{}
	projections := &flaky{failures: 2}
	c, _ := consumer(src, projections, &parker{}, nil)

	if err := c.Process(context.Background(), priceMessage(7, tenantHeader("t1"))); err != nil {
		t.Fatal(err)
	}

	if len(projections.prices) != 1 || len(src.commits) != 1 {
		t.Fatalf("после повторов обработано и зафиксировано: %+v %v", projections.prices, src.commits)
	}
}

func TestConsumerParksAfterAttemptsWhenStorageReady(t *testing.T) {
	src := &source{}
	park := &parker{}
	c, _ := consumer(src, &flaky{failures: -1}, park, nil)

	if err := c.Process(context.Background(), priceMessage(8, tenantHeader("t1"))); err != nil {
		t.Fatal(err)
	}

	if len(park.events) != 1 || park.events[0].Reason != kafka.ReasonExhausted || park.events[0].Attempts != 3 ||
		park.events[0].TenantID != "t1" {
		t.Fatalf("исчерпанные попытки — в отравленные с магазином: %+v", park.events)
	}
	if len(src.commits) != 1 {
		t.Fatalf("после парковки позиция фиксируется: %v", src.commits)
	}
}

func TestConsumerWaitsWhileStorageDown(t *testing.T) {
	src := &source{}
	park := &parker{}
	c, _ := consumer(src, &flaky{failures: -1}, park, errors.New("база недоступна"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	if err := c.Process(ctx, priceMessage(9, tenantHeader("t1"))); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("при недоступной базе сообщение ждёт: %v", err)
	}
	if len(park.events) != 0 || len(src.commits) != 0 {
		t.Fatalf("ни парковки, ни коммита: %+v %v", park.events, src.commits)
	}
}

func TestConsumerRetriesFailedParking(t *testing.T) {
	src := &source{}
	park := &parker{failures: 2}
	c, _ := consumer(src, &recorder{}, park, nil)

	if err := c.Process(context.Background(), priceMessage(10)); err != nil {
		t.Fatal(err)
	}

	if len(park.events) != 1 || len(src.commits) != 1 {
		t.Fatalf("парковка повторяется до записи, коммит — после: %+v %v", park.events, src.commits)
	}
}

func TestConsumerRunStopsOnCancel(t *testing.T) {
	src := &source{messages: []kafkago.Message{priceMessage(1, tenantHeader("t1")), priceMessage(2)}}
	rec := &recorder{}
	c, _ := consumer(src, rec, &parker{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- c.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for {
		src.mu.Lock()
		committed := len(src.commits)
		src.mu.Unlock()

		if committed == 2 || time.Now().After(deadline) {
			break
		}

		time.Sleep(time.Millisecond)
	}

	cancel()

	if err := <-done; err != nil {
		t.Fatalf("остановка по отмене — без ошибки: %v", err)
	}
	if len(src.commits) != 2 {
		t.Fatalf("оба сообщения зафиксированы: %v", src.commits)
	}
}

func TestFullTopicsUsePrefix(t *testing.T) {
	router := kafka.NewRouter(&recorder{}, &recorder{})

	topics := kafka.FullTopics("shop", router)
	sort.Strings(topics)

	if len(topics) != 8 || topics[0] != "shop.catalog.product.price-updated" || topics[7] != "shop.stock.item.updated" {
		t.Fatalf("полные имена топиков: %v", topics)
	}

	if bare := kafka.FullTopics("", router); len(bare) != 8 || strings.HasPrefix(bare[0], ".") {
		t.Fatalf("без префикса установки: %v", bare)
	}
}

func sum(t *testing.T, registry *prometheus.Registry, name string) float64 {
	t.Helper()

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}

	total := 0.0
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			total += metric.GetCounter().GetValue()
		}
	}

	return total
}
