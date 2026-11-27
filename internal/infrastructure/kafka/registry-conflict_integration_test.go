//go:build integration

package kafka_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	infrakafka "github.com/proshop-app/proshop-marketplace/internal/infrastructure/kafka"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

type registryReleaseSource struct {
	*infrakafka.Reader
	released chan kafkago.Message
}

func (s *registryReleaseSource) Release(ctx context.Context, message kafkago.Message) {
	s.Reader.Release(ctx, message)
	s.released <- message
}

func TestRegistrySlugConflictIsParkedAndPartitionContinues(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	clock := fake.NewClock(time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC))
	registry := appparticipant.NewRegistry(uow, clock, &fake.IDs{})
	if err := registry.Apply(ctx, appparticipant.RegistryEvent{
		EventID: "seed",
		Event: participant.RegistryEvent{
			TenantID: "t2",
			Slug:     "t2",
			Status:   participant.TenantActive,
			At:       clock.Now(),
		},
	}); err != nil {
		t.Fatal(err)
	}
	prefix := unique("registry-conflict-")
	topic := prefix + "." + kafka.TopicTenantCreated
	group := unique("registry-conflict-group-")
	createTopics(t, topic)
	t.Cleanup(func() {
		client := &kafkago.Client{Addr: kafkago.TCP(brokers()...), Timeout: 5 * time.Second}
		result, err := client.DeleteGroups(ctx, &kafkago.DeleteGroupsRequest{GroupIDs: []string{group}})
		if err == nil {
			err = result.Errors[group]
		}
		if err != nil && !errors.Is(err, kafkago.GroupIdNotFound) {
			t.Errorf("удаление группы: %v", err)
		}
	})
	produce(
		t,
		kafkago.Message{
			Topic: topic,
			Key:   []byte("key"),
			Value: []byte(`{"tenantId":"t1","slug":"t2","status":"active","at":"2026-11-05T10:00:00Z"}`),
		},
		kafkago.Message{
			Topic: topic,
			Key:   []byte("key"),
			Value: []byte(`{"tenantId":"t3","slug":"t3","status":"active","at":"2026-11-05T10:00:00Z"}`),
		},
	)
	reader := infrakafka.NewReader(infrakafka.Config{
		Brokers: brokers(),
		GroupID: group,
		Topics:  []string{topic},
		Logger:  quiet,
	})
	defer func() { _ = reader.Close() }()
	source := &registryReleaseSource{Reader: reader, released: make(chan kafkago.Message, 2)}
	consumer := kafka.NewConsumer(
		source,
		kafka.NewRouter(nil, registry),
		messaging.NewParker(uow),
		kafka.ConsumerConfig{
			TopicPrefix: prefix,
			Workers:     1,
			MaxAttempts: 1,
			Backoff:     func(int) time.Duration { return time.Minute },
			Ready:       pool.Ping,
			Now:         clock.Now,
		},
		quiet,
		kafka.NewConsumerMetrics(prometheus.NewRegistry()),
	)
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- consumer.Run(runCtx) }()
	partitions := map[int]bool{}
	for range 2 {
		select {
		case message := <-source.released:
			partitions[message.Partition] = true
		case <-runCtx.Done():
			t.Fatal("партиция не обработана одним воркером")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(partitions) != 1 {
		t.Fatal("тестовые сообщения должны быть в одной партиции")
	}
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		events, _, err := tx.Parked().List(ctx, ports.EventFilter{Limit: 10})
		if err != nil {
			return err
		}
		if len(events) != 1 || events[0].Reason != "participant.slug_taken" || events[0].Attempts != 1 {
			t.Errorf("ожидалась одна парковка вместо повторов: %+v", events)
		}
		_, err = tx.Registry().Get(ctx, "t3")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
