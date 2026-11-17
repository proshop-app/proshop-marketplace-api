//go:build integration

package kafka_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
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
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	infrakafka "github.com/proshop-app/proshop-marketplace/internal/infrastructure/kafka"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	measureMessages = 2000
	measureTenants  = 16
	setMeasureTrust = `UPDATE participants SET trust = 'trusted'`
)

type intakeQueries struct {
	statements atomic.Int64
	duration   atomic.Int64
}

type intakeQueryStarted struct{}

type measuredProjection struct {
	*appcatalog.Projector
	total    atomic.Int64
	duration atomic.Int64
	done     chan struct{}
}

type measuredSource struct {
	*infrakafka.Reader
	queries     *intakeQueries
	started     chan time.Time
	once        sync.Once
	commits     atomic.Int64
	commitTime  atomic.Int64
	commitMax   atomic.Int64
	failures    atomic.Int64
	cancelled   atomic.Int64
	lastFailure string
}

func (s *measuredSource) Run(ctx context.Context, consume func(context.Context) error) error {
	return s.Reader.Run(ctx, func(ctx context.Context) error {
		s.once.Do(func() {
			s.queries.statements.Store(0)
			s.queries.duration.Store(0)
			s.started <- time.Now()
		})
		return consume(ctx)
	})
}

func (s *measuredSource) Commit(ctx context.Context, messages ...kafkago.Message) error {
	started := time.Now()
	err := s.Reader.Commit(ctx, messages...)
	elapsed := time.Since(started).Nanoseconds()
	s.commits.Add(1)
	s.commitTime.Add(elapsed)
	for previous := s.commitMax.Load(); elapsed > previous; previous = s.commitMax.Load() {
		if s.commitMax.CompareAndSwap(previous, elapsed) {
			break
		}
	}
	if err != nil {
		s.lastFailure = err.Error()
		if ctx.Err() != nil {
			s.cancelled.Add(1)
		} else {
			s.failures.Add(1)
		}
	}
	return err
}

func (q *intakeQueries) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	sql := strings.TrimSpace(data.SQL)
	if !strings.EqualFold(sql, "begin") && !strings.EqualFold(sql, "commit") && !strings.EqualFold(sql, "rollback") {
		q.statements.Add(1)
	}
	return context.WithValue(ctx, intakeQueryStarted{}, time.Now())
}

func (q *intakeQueries) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	q.duration.Add(time.Since(ctx.Value(intakeQueryStarted{}).(time.Time)).Nanoseconds())
}

func (p *measuredProjection) applied(started time.Time, err error) error {
	p.duration.Add(time.Since(started).Nanoseconds())
	if err == nil && p.total.Add(1) == measureMessages {
		close(p.done)
	}
	return err
}

func (p *measuredProjection) ApplyProduct(ctx context.Context, event appcatalog.ProductEvent) error {
	return p.applied(time.Now(), p.Projector.ApplyProduct(ctx, event))
}

func (p *measuredProjection) ApplyPrice(ctx context.Context, event appcatalog.PriceEvent) error {
	return p.applied(time.Now(), p.Projector.ApplyPrice(ctx, event))
}

func (p *measuredProjection) ApplyStock(ctx context.Context, event appcatalog.StockEvent) error {
	return p.applied(time.Now(), p.Projector.ApplyStock(ctx, event))
}

// Запускается явно: приём включает настоящий Kafka и COMMIT PostgreSQL,
// публикация и время вступления в группу не входят в секундомер.
func TestIntakeThroughput(t *testing.T) {
	if os.Getenv("INTAKE_MEASURE") != "1" {
		t.Skip("замер: INTAKE_MEASURE=1")
	}
	for _, workers := range []int{1, 8} {
		for _, kind := range []string{"product", "price", "stock"} {
			t.Run(fmt.Sprintf("%s/workers=%d", kind, workers), func(t *testing.T) {
				measureIntake(t, kind, workers)
			})
		}
	}
}

func measureIntake(t *testing.T, kind string, workers int) {
	t.Helper()
	pool, queries, uow := intakeDatabase(t)
	moderation := appmoderation.NewService(uow, clock.System{}, random.IDs{}, appmoderation.Config{})
	projection := &measuredProjection{
		Projector: appcatalog.NewProjector(uow, clock.System{}, moderation),
		done:      make(chan struct{}),
	}
	topic := unique("measure-") + "." + intakeTopic(kind)
	createTopics(t, topic)
	messages := intakeMessages(kind, topic)
	produce(t, messages...)
	router := kafka.NewRouter(projection, appparticipant.NewRegistry(uow, clock.System{}, random.IDs{}))
	prefix := strings.TrimSuffix(topic, "."+intakeTopic(kind))
	reader := infrakafka.NewReader(infrakafka.Config{
		Brokers: brokers(),
		GroupID: unique("measure-group-"),
		Topics:  []string{topic},
		Logger:  quiet,
	})
	defer func() { _ = reader.Close() }()
	source := &measuredSource{Reader: reader, queries: queries, started: make(chan time.Time, 1)}
	consumer := kafka.NewConsumer(
		source,
		router,
		messaging.NewParker(uow),
		kafka.ConsumerConfig{
			Workers:     workers,
			TopicPrefix: prefix,
			MaxAttempts: 3,
			Ready:       pool.Ping,
			Backoff:     messaging.Backoff,
			Now:         time.Now,
		},
		quiet,
		prometheus.NewRegistry(),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- consumer.Run(ctx) }()
	var started time.Time
	select {
	case started = <-source.started:
	case <-ctx.Done():
		t.Fatal("не началось назначение партиций")
	}
	select {
	case <-projection.done:
	case <-ctx.Done():
		t.Fatal("не завершился приём", projection.total.Load())
	}
	elapsed := time.Since(started)
	statements := queries.statements.Load()
	cancel()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	t.Logf(
		"MEASURE kind=%s workers=%d n=%d elapsed=%s rate=%.1f msg/s sql=%.2f/msg pool=16",
		kind,
		workers,
		measureMessages,
		elapsed,
		measureMessages/elapsed.Seconds(),
		float64(statements)/measureMessages,
	)
	t.Logf(
		"TIMING db-handlers-sum=%s sql-sum=%s kafka-commits=%d sum=%s max=%s failures=%d",
		time.Duration(projection.duration.Load()),
		time.Duration(queries.duration.Load()),
		source.commits.Load(),
		time.Duration(source.commitTime.Load()),
		time.Duration(source.commitMax.Load()),
		source.failures.Load(),
	)
	if source.cancelled.Load() > 0 || source.lastFailure != "" {
		t.Logf("COMMIT cancelled=%d last-error=%q", source.cancelled.Load(), source.lastFailure)
	}
}

func intakeDatabase(t *testing.T) (*pgxpool.Pool, *intakeQueries, ports.UnitOfWork) {
	t.Helper()
	base := pgtest.Pool(t)
	cfg := base.Config().Copy()
	cfg.MaxConns = 16
	queries := &intakeQueries{}
	cfg.ConnConfig.Tracer = queries
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	uow := postgres.NewUnitOfWork(pool)
	seedIntake(t, uow)
	if _, err := pool.Exec(context.Background(), setMeasureTrust); err != nil {
		t.Fatal(err)
	}
	return pool, queries, uow
}

func intakeTopic(kind string) string {
	switch kind {
	case "product":
		return kafka.TopicProductProjected
	case "price":
		return kafka.TopicProductPriceUpdated
	default:
		return kafka.TopicStockItemUpdated
	}
}

func intakeMessages(kind, topic string) []kafkago.Message {
	messages := make([]kafkago.Message, measureMessages)
	for i := range messages {
		tenant, product := fmt.Sprintf("t%d", i%measureTenants), fmt.Sprintf("p%d", i)
		value := fmt.Sprintf(`{"id":%q,"price":100}`, product)
		if kind == "stock" {
			value = fmt.Sprintf(`{"productId":%q,"quantity":5,"reserved":1,"price":100}`, product)
		}
		if kind == "product" {
			value = fmt.Sprintf(
				`{"version":1,"tenantId":%q,"productId":%q,"revision":1,"operation":"upsert",`+
					`"product":{"id":%q,"name":"Товар","price":100,"conditions":{"isVisible":true},`+
					`"marketplace":{"categoryId":"c1","published":true,"attributes":{}}}}`,
				tenant,
				product,
				product,
			)
		}
		messages[i] = kafkago.Message{
			Topic:   topic,
			Key:     []byte(fmt.Sprintf(`[%q,%q]`, tenant, product)),
			Value:   []byte(value),
			Time:    time.Now(),
			Headers: []kafkago.Header{{Key: kafka.HeaderTenant, Value: []byte(tenant)}},
		}
	}
	return messages
}

func seedIntake(t *testing.T, uow ports.UnitOfWork) {
	t.Helper()
	ctx := context.Background()
	registry := appparticipant.NewRegistry(uow, clock.System{}, random.IDs{})
	for i := 0; i < measureTenants; i++ {
		tenant := fmt.Sprintf("t%d", i)
		err := registry.Apply(ctx, appparticipant.RegistryEvent{EventID: tenant, Event: participant.RegistryEvent{
			TenantID: ids.TenantID(tenant),
			Slug:     tenant,
			Name:     "Магазин",
			Status:   participant.TenantActive,
			At:       time.Now(),
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		definition := taxonomy.Definition{
			Version:    1,
			Schemas:    map[string]taxonomy.Schema{"s1": {Attributes: []string{}, Optional: []string{}}},
			Categories: []taxonomy.Category{{ID: "c1", Slug: "c1", Name: "Категория", Schema: "s1"}},
		}
		return tx.Taxonomy().Save(ctx, definition, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if _, err := tx.Taxonomy().Current(ctx); err != nil {
			return err
		}
		_, err := tx.ModerationRules().Current(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
