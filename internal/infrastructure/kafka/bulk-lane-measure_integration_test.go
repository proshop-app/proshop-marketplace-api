//go:build integration

package kafka_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	kafkago "github.com/segmentio/kafka-go"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	infrakafka "github.com/proshop-app/proshop-marketplace/internal/infrastructure/kafka"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

const (
	laneWaveSize   = 4000
	laneProbeCount = 10
	laneProbeID    = "p-point"
	laneProbeQuery = `SELECT revision FROM catalog_products WHERE tenant_id = $1 AND product_id = $2`
)

type waveProjection struct {
	*appcatalog.Projector
	applied  atomic.Int64
	finished chan time.Time
}

func (p *waveProjection) ApplyProduct(ctx context.Context, event appcatalog.ProductEvent) error {
	err := p.Projector.ApplyProduct(ctx, event)
	if err == nil && event.Snapshot.ProductID != laneProbeID && p.applied.Add(1) == laneWaveSize {
		p.finished <- time.Now()
	}
	return err
}

// TestBulkLaneIntake сравнивает раскладки в общем пуле. Точечный снимок
// измеряется от публикации до чтения новой ревизии в PostgreSQL.
func TestBulkLaneIntake(t *testing.T) {
	if os.Getenv("INTAKE_MEASURE") != "1" {
		t.Skip("замер: INTAKE_MEASURE=1")
	}
	for _, poolSize := range []int32{10, 16} {
		for _, bulkWorkers := range []int{1, 2, 3, 4} {
			t.Run(fmt.Sprintf("pool=%d/bulk=%d", poolSize, bulkWorkers), func(t *testing.T) {
				measureBulkLane(t, poolSize, bulkWorkers)
			})
		}
	}
}

func measureBulkLane(t *testing.T, poolSize int32, bulkWorkers int) {
	t.Helper()
	pool, queries, uow := intakeDatabase(t, poolSize)
	moderation := appmoderation.NewService(uow, clock.System{}, random.IDs{}, appmoderation.Config{})
	projection := &waveProjection{
		Projector: appcatalog.NewProjector(uow, clock.System{}, moderation),
		finished:  make(chan time.Time, 1),
	}
	router := kafka.NewRouter(projection, nil)
	prefix, group := unique("measure-lanes"), unique("measure-group-")
	regular, bulk := kafka.SubscriptionTopics(prefix, router)
	point := prefix + "." + kafka.TopicProductProjected
	createTopics(t, point, bulk)
	produce(t, intakeMessages("product", bulk, laneWaveSize)...)

	pointReader := laneMeasureReader(group, regular)
	bulkReader := laneMeasureReader(group+"-bulk", []string{bulk})
	bulkSource := &measuredSource{Reader: bulkReader, queries: queries, started: make(chan time.Time, 1)}
	metrics := kafka.NewConsumerMetrics(prometheus.NewRegistry())
	cfg := kafka.ConsumerConfig{
		TopicPrefix: prefix,
		Workers:     int(poolSize/2) - bulkWorkers,
		MaxAttempts: 3,
		Ready:       pool.Ping,
		Backoff:     messaging.Backoff,
		Now:         time.Now,
	}
	parker := messaging.NewParker(uow)
	startLaneMeasure(t, pointReader, kafka.NewConsumer(pointReader, router, parker, cfg, quiet, metrics))
	writer := &kafkago.Writer{
		Addr:         kafkago.TCP(brokers()...),
		Balancer:     &kafkago.Hash{},
		RequiredAcks: kafkago.RequireAll,
		BatchSize:    1,
	}
	t.Cleanup(func() { _ = writer.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	probeLaneRevision(ctx, t, pool, writer, point, 1)

	regularWorkers := cfg.Workers
	cfg.Workers = bulkWorkers
	startLaneMeasure(t, bulkReader, kafka.NewConsumer(bulkSource, router, parker, cfg, quiet, metrics))
	started := laneMeasureTime(ctx, t, bulkSource.started, "назначение массовой группы")
	latencies := measureLaneProbes(ctx, t, pool, writer, point, projection)
	finished := laneMeasureTime(ctx, t, projection.finished, "приём массовой волны")
	if parked := count(t, pool, countIntakeParked); parked != 0 {
		t.Fatalf("в парковке %d событий замера", parked)
	}
	elapsed := finished.Sub(started)
	t.Logf(
		"LANES pool=%d regular=%d bulk=%d n=%d elapsed=%s rate=%.1f msg/s point=%v",
		poolSize,
		regularWorkers,
		bulkWorkers,
		laneWaveSize,
		elapsed,
		laneWaveSize/elapsed.Seconds(),
		latencies,
	)
}

func laneMeasureReader(group string, topics []string) *infrakafka.Reader {
	return infrakafka.NewReader(infrakafka.Config{
		Brokers: brokers(),
		GroupID: group,
		Topics:  topics,
		Logger:  quiet,
	})
}

func startLaneMeasure(t *testing.T, reader *infrakafka.Reader, consumer *kafka.Consumer) {
	t.Helper()
	budget := ports.DatabaseBudget{Statement: 2 * time.Second, Transaction: 5 * time.Second}
	ctx, cancel := context.WithCancel(ports.WithDatabaseBudget(context.Background(), budget))
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	})
}

func laneMeasureTime(ctx context.Context, t *testing.T, stamps <-chan time.Time, what string) time.Time {
	t.Helper()
	select {
	case stamp := <-stamps:
		return stamp
	case <-ctx.Done():
		t.Fatalf("%s: %v", what, ctx.Err())
		return time.Time{}
	}
}

func measureLaneProbes(
	ctx context.Context,
	t *testing.T,
	pool *pgxpool.Pool,
	writer *kafkago.Writer,
	point string,
	projection *waveProjection,
) []time.Duration {
	t.Helper()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	latencies := make([]time.Duration, 0, laneProbeCount)
	for i := range laneProbeCount {
		target := int64(laneWaveSize * (i + 1) / (laneProbeCount + 1))
		for projection.applied.Load() < target {
			select {
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatalf("массовый приём остановился на %d событиях: %v", projection.applied.Load(), ctx.Err())
			}
		}
		latencies = append(latencies, probeLaneRevision(ctx, t, pool, writer, point, int64(i+2)))
		if projection.applied.Load() >= laneWaveSize {
			t.Fatal("волна завершилась до наблюдения точечной правки")
		}
	}
	return latencies
}

func probeLaneRevision(
	ctx context.Context,
	t *testing.T,
	pool *pgxpool.Pool,
	writer *kafkago.Writer,
	topic string,
	revision int64,
) time.Duration {
	t.Helper()
	msg := laneSnapshot{revision: revision, published: true, variant: "v1"}.message(topic, laneProbeID)
	started := time.Now()
	if err := writer.WriteMessages(ctx, msg); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		var seen int64
		err := pool.QueryRow(ctx, laneProbeQuery, "t0", laneProbeID).Scan(&seen)
		if err == nil && seen == revision {
			return time.Since(started)
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("точечная ревизия %d не видна: %v", revision, ctx.Err())
		}
	}
}
