//go:build integration

package kafka_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	kafkago "github.com/segmentio/kafka-go"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	infrakafka "github.com/proshop-app/proshop-marketplace/internal/infrastructure/kafka"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

type stoppingSource struct {
	*infrakafka.Reader
	applied chan struct{}
	final   chan error
}

func (s *stoppingSource) Release(ctx context.Context, message kafkago.Message) {
	s.Reader.Release(ctx, message)
	s.applied <- struct{}{}
}

func (s *stoppingSource) Commit(ctx context.Context, messages ...kafkago.Message) error {
	// Таймер не должен забрать проверяемый префикс, даже если CI работает медленно.
	if _, final := ctx.Deadline(); !final {
		return errors.New("обычная фиксация временно недоступна")
	}
	err := s.Reader.Commit(ctx, messages...)
	s.final <- err
	return err
}

func TestConsumerFinalCommitReachesBrokerAfterProcessCancellation(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	prefix := unique("shutdown-")
	topic := prefix + "." + kafka.TopicProductPriceUpdated
	group := unique("shutdown-group-")
	createTopics(t, topic)
	message := intakeMessages("price", topic, 1)[0]
	produce(t, message)
	reader := infrakafka.NewReader(infrakafka.Config{
		Brokers: brokers(),
		GroupID: group,
		Topics:  []string{topic},
		Logger:  quiet,
	})
	defer func() { _ = reader.Close() }()
	source := &stoppingSource{Reader: reader, applied: make(chan struct{}, 1), final: make(chan error, 1)}
	router := kafka.NewRouter(appcatalog.NewProjector(uow, clock.System{}, nil), nil)
	consumer := kafka.NewConsumer(
		source,
		router,
		messaging.NewParker(uow),
		kafka.ConsumerConfig{
			TopicPrefix: prefix,
			Workers:     1,
			MaxAttempts: 1,
			Ready:       pool.Ping,
			Backoff:     messaging.Backoff,
			Now:         time.Now,
		},
		quiet,
		kafka.NewConsumerMetrics(prometheus.NewRegistry()),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	select {
	case <-source.applied:
	case <-time.After(30 * time.Second):
		t.Fatal("сообщение не принято")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("финальный коммит задержал остановку")
	}
	select {
	case err := <-source.final:
		if err != nil {
			t.Fatal("финальный коммит не дошёл до брокера", err)
		}
	default:
		t.Fatal("нет финального коммита")
	}
	assertStoppedOffset(t, group, topic)
}

func assertStoppedOffset(t *testing.T, group, topic string) {
	t.Helper()
	client := &kafkago.Client{Addr: kafkago.TCP(brokers()...), Timeout: 5 * time.Second}
	response, err := client.OffsetFetch(context.Background(), &kafkago.OffsetFetchRequest{
		GroupID: group,
		Topics:  map[string][]int{topic: {0, 1, 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	committed := 0
	for _, partition := range response.Topics[topic] {
		if partition.Error != nil {
			t.Fatal(partition.Error)
		}
		if partition.CommittedOffset == 1 {
			committed++
		} else if partition.CommittedOffset != -1 {
			t.Fatalf("зафиксировано незавершённое смещение: %+v", partition)
		}
	}
	if committed != 1 {
		t.Fatal("брокер не сохранил финальный префикс", response.Topics)
	}
}
