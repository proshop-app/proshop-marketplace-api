//go:build integration

package kafka_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	kafkago "github.com/segmentio/kafka-go"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	infrakafka "github.com/proshop-app/proshop-marketplace/internal/infrastructure/kafka"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

const countIntakeParked = `SELECT count(*) FROM parked_events`

func TestIntakeSQLCounts(t *testing.T) {
	_, queries, uow := intakeDatabase(t, measurePool)
	moderation := appmoderation.NewService(uow, clock.System{}, random.IDs{}, appmoderation.Config{})
	projector := appcatalog.NewProjector(uow, clock.System{}, moderation)
	ctx := context.Background()
	message := intakeMessages("product", "topic", 1)[0]
	snapshot, err := kafka.DecodeProjectionSnapshot(message.Value, message.Time)
	if err != nil {
		t.Fatal(err)
	}
	check := func(name string, want int64, apply func() error) {
		t.Helper()
		queries.statements.Store(0)
		if err := apply(); err != nil {
			t.Fatal(err)
		}
		if got := queries.statements.Load(); got != want {
			t.Fatalf("%s: SQL %d, ожидалось %d", name, got, want)
		}
		t.Logf("SQL %s: %d (+ BEGIN/COMMIT)", name, want)
	}
	check("product", 9, func() error {
		return projector.ApplyProduct(ctx, appcatalog.ProductEvent{Snapshot: snapshot})
	})
	snapshot.Revision++
	snapshot.Price, err = snapshot.Price.Add(snapshot.Price)
	if err != nil {
		t.Fatal(err)
	}
	check("product/price-only", 5, func() error {
		return projector.ApplyProduct(ctx, appcatalog.ProductEvent{Snapshot: snapshot})
	})
	snapshot.Revision--
	check("product/stale", 1, func() error {
		return projector.ApplyProduct(ctx, appcatalog.ProductEvent{Snapshot: snapshot})
	})
	check("stock", 3, func() error {
		return projector.ApplyStock(ctx, appcatalog.StockEvent{
			TenantID:  snapshot.TenantID,
			ProductID: snapshot.ProductID,
			Stock:     catalog.StockSignal{Available: 5, At: message.Time},
		})
	})
}

func TestIntakeBeforeRegistryPreservesDataAndReevaluates(t *testing.T) {
	pool, _, uow := intakeDatabase(t, measurePool)
	moderation := appmoderation.NewService(uow, clock.System{}, random.IDs{}, appmoderation.Config{})
	projector := appcatalog.NewProjector(uow, clock.System{}, moderation)
	registry := appparticipant.NewRegistry(uow, clock.System{}, random.IDs{})
	reader := infrakafka.NewReader(infrakafka.Config{})
	defer func() { _ = reader.Close() }()
	consumer := kafka.NewConsumer(
		reader,
		kafka.NewRouter(projector, registry),
		messaging.NewParker(uow),
		kafka.ConsumerConfig{MaxAttempts: 1, Ready: pool.Ping, Backoff: messaging.Backoff, Now: time.Now},
		quiet,
		kafka.NewConsumerMetrics(prometheus.NewRegistry()),
	)
	messages := []kafkago.Message{
		intakeMessages("product", kafka.TopicProductProjected, 1)[0],
		intakeMessages("stock", kafka.TopicStockItemUpdated, 1)[0],
	}
	for i, message := range messages {
		message.Value = []byte(strings.ReplaceAll(string(message.Value), `"t0"`, `"late"`))
		message.Key = []byte(`["late","p0"]`)
		message.Headers = []kafkago.Header{{Key: kafka.HeaderTenant, Value: []byte("late")}}
		message.Offset = int64(i)
		if err := consumer.Process(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if _, err := tx.Products().Get(ctx, "late", "p0"); err != nil {
			return err
		}
		_, err := tx.Signals().Get(ctx, "late", "p0")
		return err
	}); err != nil {
		t.Fatal("проекции без зеркала не сохранены", err)
	}
	var parked int
	if err := pool.QueryRow(ctx, countIntakeParked).Scan(&parked); err != nil || parked != 0 {
		t.Fatalf("отсутствие зеркала отправило в парковку: %d, %v", parked, err)
	}
	at := time.Now()
	for i, status := range []participant.TenantStatus{
		participant.TenantActive,
		participant.TenantSuspended,
		participant.TenantDeleted,
	} {
		err := registry.Apply(ctx, appparticipant.RegistryEvent{
			EventID: fmt.Sprint("registry", i),
			Event: participant.RegistryEvent{
				TenantID: "late",
				Slug:     "late",
				Name:     "Магазин",
				Status:   status,
				At:       at.Add(time.Duration(i) * time.Second),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := moderation.Reevaluate(ctx, "late"); err != nil {
				t.Fatal(err)
			}
			checkIntakeRegistry(t, uow, status)
		}
		snapshot, err := kafka.DecodeProjectionSnapshot(
			[]byte(strings.ReplaceAll(string(messages[0].Value), `"t0"`, `"late"`)),
			at,
		)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Revision = int64(i + 2)
		if err := projector.ApplyProduct(ctx, appcatalog.ProductEvent{
			Snapshot: snapshot,
		}); err != nil {
			t.Fatal("товар после изменения реестра не принят", err)
		}
		checkIntakeRegistry(t, uow, status)
	}
}

func checkIntakeRegistry(t *testing.T, uow ports.UnitOfWork, status participant.TenantStatus) {
	t.Helper()
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		state, err := tx.Offers().Offer(ctx, "late", "p0")
		if err != nil {
			return err
		}
		if state.Verdict == nil || state.Tenant.Status() != status {
			t.Fatalf("переоценка или состояние зеркала потеряны: %+v", state)
		}
		if status != participant.TenantActive {
			if participant.VisibilityOf(state.Participant, state.Tenant).Visible {
				t.Fatal("приостановленный или удалённый магазин виден после нового товара")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
