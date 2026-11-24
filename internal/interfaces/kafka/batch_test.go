package kafka_test

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	kafkago "github.com/segmentio/kafka-go"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// batchProjection держит первый снимок, пока очередь не наберёт остальные, и записывает пачки.
type batchProjection struct {
	kafka.Projections
	mu      sync.Mutex
	gate    chan struct{}
	batches [][]string
	applied []string
	fail    func([]appcatalog.ProductEvent) error
}

type syncParker struct {
	mu     sync.Mutex
	events []ports.ParkedEvent
}

func (p *batchProjection) ApplyProduct(ctx context.Context, event appcatalog.ProductEvent) error {
	return p.ApplyProducts(ctx, []appcatalog.ProductEvent{event})
}

func (p *batchProjection) ApplyProducts(ctx context.Context, events []appcatalog.ProductEvent) error {
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	products := make([]string, 0, len(events))
	for _, event := range events {
		products = append(products, string(event.Snapshot.ProductID))
	}
	p.batches = append(p.batches, products)
	if p.fail != nil {
		if err := p.fail(events); err != nil {
			return err
		}
	}
	p.applied = append(p.applied, products...)
	return nil
}

func (p *batchProjection) snapshot() ([][]string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.batches), slices.Clone(p.applied)
}

func (p *syncParker) Park(_ context.Context, event ports.ParkedEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return nil
}

func (p *syncParker) parked() []ports.ParkedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.events)
}

// runBatched запускает приём одним воркером: первое сообщение держит воркер, остальные копятся
// в очереди и уходят следующей пачкой.
func runBatched(t *testing.T, projection *batchProjection, park kafka.Parker, messages ...kafkago.Message) {
	t.Helper()
	source := bufferedSource(messages...)
	cfg := kafka.ConsumerConfig{
		TopicPrefix: "shop",
		Workers:     1,
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return time.Millisecond },
		Ready:       func(context.Context) error { return nil },
		Now:         func() time.Time { return published },
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := kafka.NewConsumerMetrics(prometheus.NewRegistry())
	consumer := kafka.NewConsumer(source, kafka.NewRouter(projection, &recorder{}), park, cfg, logger, metrics)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	close(projection.gate)
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case committed := <-source.commits:
			if committed[len(committed)-1].Offset == messages[len(messages)-1].Offset {
				cancel()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				return
			}
		case <-time.After(time.Until(deadline)):
			cancel()
			t.Fatal("приём не зафиксировал последнее сообщение")
		}
	}
}

func TestConsumerAppliesDistinctProductsAsOneBatch(t *testing.T) {
	projection := &batchProjection{gate: make(chan struct{})}
	messages := []kafkago.Message{productMessage("p0", 1, 0)}
	for n, product := range []string{"p1", "p2", "p3", "p1"} {
		messages = append(messages, productMessage(product, int64(n+2), int64(n+1)))
	}
	runBatched(t, projection, &syncParker{}, messages...)
	batches, applied := projection.snapshot()
	widest := 0
	for _, batch := range batches {
		widest = max(widest, len(batch))
		// Следующее сообщение ключа ждёт завершения предыдущего: в одну пачку ключ не попадает дважды.
		if len(slices.Compact(slices.Sorted(slices.Values(batch)))) != len(batch) {
			t.Fatal("ключ дважды в пачке", batches)
		}
	}
	if widest < 2 || len(applied) != 5 {
		t.Fatal("снимки, накопленные за занятым воркером, должны уйти одной пачкой", batches)
	}
}

func TestConsumerParksBrokenMessageAndAppliesRestOfBatch(t *testing.T) {
	projection := &batchProjection{gate: make(chan struct{})}
	broken := productMessage("p2", 1, 2)
	broken.Value = []byte("{")
	messages := []kafkago.Message{
		productMessage("p0", 1, 0),
		productMessage("p1", 1, 1),
		broken,
		productMessage("p3", 1, 3),
	}
	park := &syncParker{}
	runBatched(t, projection, park, messages...)
	batches, applied := projection.snapshot()
	parked := park.parked()
	if len(batches) > 3 || slices.Contains(applied, "p2") || len(applied) != 3 || len(parked) != 1 ||
		parked[0].Offset != 2 {
		t.Fatal("сломанное сообщение — в отравленные, остальные применены", applied, parked)
	}
}

func TestConsumerRetriesRolledBackBatchOneByOne(t *testing.T) {
	invalid := errs.New(errs.Invalid, "catalog.invalid_snapshot", "снимок не по контракту")
	projection := &batchProjection{gate: make(chan struct{})}
	// Пачка с p2 откатывается целиком; по одному p2 уходит в отравленные, остальные применяются.
	projection.fail = func(events []appcatalog.ProductEvent) error {
		for _, event := range events {
			if event.Snapshot.ProductID == "p2" {
				return invalid
			}
		}
		return nil
	}
	messages := []kafkago.Message{productMessage("p0", 1, 0)}
	for n, product := range []string{"p1", "p2", "p3"} {
		messages = append(messages, productMessage(product, 1, int64(n+1)))
	}
	park := &syncParker{}
	runBatched(t, projection, park, messages...)
	batches, applied := projection.snapshot()
	rolledBack := slices.IndexFunc(batches, func(batch []string) bool {
		return len(batch) > 1 && slices.Contains(batch, "p2")
	})
	if rolledBack < 0 {
		t.Fatal("p2 должен попасть в пачку", batches)
	}
	retried := batches[rolledBack+1:]
	for _, product := range batches[rolledBack] {
		if !slices.ContainsFunc(retried, func(batch []string) bool { return slices.Equal(batch, []string{product}) }) {
			t.Fatal("сообщение откатившейся пачки не повторено по одному", product, batches)
		}
	}
	slices.Sort(applied)
	parked := park.parked()
	if !slices.Equal(applied, []string{"p0", "p1", "p3"}) || len(parked) != 1 || parked[0].Offset != 2 ||
		parked[0].Reason != invalid.Key() {
		t.Fatal("откатившаяся пачка повторена по одному", applied, parked)
	}
}
