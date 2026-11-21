package kafka_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	kafkago "github.com/segmentio/kafka-go"

	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

type registryFunc func(context.Context, appparticipant.RegistryEvent) error

func (f registryFunc) Apply(ctx context.Context, event appparticipant.RegistryEvent) error {
	return f(ctx, event)
}

func TestConsumerIsolatesRegistryTenants(t *testing.T) {
	for _, registry := range []bool{true} {
		t.Run(fmt.Sprint("registry=", registry), func(t *testing.T) {
			blocked := make(chan struct{}, 1)
			independent := make(chan struct{}, 1)
			handle := func(ctx context.Context, blockedKey bool) error {
				if blockedKey {
					blocked <- struct{}{}
					<-ctx.Done()
					return ctx.Err()
				}
				independent <- struct{}{}
				return nil
			}
			registryHandler := registryFunc(func(ctx context.Context, e appparticipant.RegistryEvent) error {
				return handle(ctx, e.Event.TenantID == "t1")
			})
			router := kafka.NewRouter(&recorder{}, registryHandler)
			source := bufferedSource(isolatedMessage(registry, 0), isolatedMessage(registry, 1))
			consumer := kafka.NewConsumer(
				source,
				router,
				&parker{},
				kafka.ConsumerConfig{
					Workers:     2,
					MaxAttempts: 3,
					Backoff:     func(int) time.Duration { return time.Millisecond },
					Ready:       func(context.Context) error { return nil },
					Now:         time.Now,
				},
				slog.New(slog.NewTextHandler(io.Discard, nil)),
				kafka.NewConsumerMetrics(prometheus.NewRegistry()),
			)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- consumer.Run(ctx) }()
			await(t, blocked)
			await(t, independent)
			cancel()
			if err := await(t, done); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func isolatedMessage(registry bool, offset int64) kafkago.Message {
	var tenant, topic, key, body string
	if registry {
		tenant = fmt.Sprint("t", offset+1)
		topic = kafka.TopicTenantCreated
		key = fmt.Sprintf(`"%s"`, tenant)
		body = fmt.Sprintf(
			`{"tenantId":%q,"slug":%q,"status":"active","at":%q}`,
			tenant,
			tenant,
			published.Format(time.RFC3339),
		)
	}
	return kafkago.Message{Topic: topic, Key: []byte(key), Value: []byte(body), Time: published, Offset: offset}
}
