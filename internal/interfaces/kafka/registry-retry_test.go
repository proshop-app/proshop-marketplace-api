package kafka_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestSlugConflictParksOnceButVersionRaceRetries(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	for _, tenant := range []ids.TenantID{"t1", "t2"} {
		store.SeedTenant(participant.TenantState{
			TenantID: tenant,
			Slug:     string(tenant),
			Status:   participant.TenantActive,
			Version:  1,
		})
		store.SeedParticipant(participant.State{
			TenantID: tenant,
			Slug:     string(tenant),
			Version:  1,
		})
	}
	clock := fake.NewClock(published)
	registry := appparticipant.NewRegistry(store, clock, &fake.IDs{})
	calls := map[ids.TenantID]int{}
	handler := registryFunc(func(ctx context.Context, event appparticipant.RegistryEvent) error {
		tenant := event.Event.TenantID
		calls[tenant]++
		if tenant == "t3" && calls[tenant] == 1 {
			return participant.ErrTenantConflict
		}
		return registry.Apply(ctx, event)
	})
	readyCalls := 0
	park := &parker{failures: 1}
	consumer := kafka.NewConsumer(
		&source{},
		kafka.NewRouter(&recorder{}, handler),
		park,
		kafka.ConsumerConfig{
			MaxAttempts: 1,
			Backoff:     func(int) time.Duration { return 0 },
			Ready: func(context.Context) error {
				readyCalls++
				return nil
			},
			Now: clock.Now,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		kafka.NewConsumerMetrics(prometheus.NewRegistry()),
	)
	bad := isolatedMessage(true, 0)
	bad.Value = []byte(`{"tenantId":"t1","slug":"t2","status":"active","at":"2026-11-05T10:00:00Z"}`)
	if err := consumer.Process(ctx, bad); err != nil {
		t.Fatal(err)
	}
	if err := consumer.Process(ctx, isolatedMessage(true, 2)); err != nil {
		t.Fatal(err)
	}
	if calls["t1"] != 1 || calls["t3"] != 2 || readyCalls != 0 {
		t.Fatalf("попытки реестра: %v, проверок базы: %d", calls, readyCalls)
	}
	if len(park.events) != 1 || park.events[0].Reason != "participant.slug_taken" || park.events[0].TenantID != "t1" {
		t.Fatalf("парковка: %+v", park.events)
	}
	if store.Processed(kafka.TopicTenantCreated+":0:0") || !store.Processed(kafka.TopicTenantCreated+":0:2") {
		t.Fatal("парковка откатывает дедуп события, успех фиксирует его")
	}
}
