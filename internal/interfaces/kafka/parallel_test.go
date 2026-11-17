package kafka_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type parallelSource struct {
	messages chan kafkago.Message
	commits  chan []kafkago.Message
	run      func(context.Context, func(context.Context) error) error
}

type controlledProjection struct {
	kafka.Projections
	product func(context.Context, appcatalog.ProductEvent) error
	price   func(context.Context, appcatalog.PriceEvent) error
}

type waitingParker struct {
	entered  chan struct{}
	release  chan struct{}
	attempts atomic.Int32
	durable  atomic.Bool
}

func (p *waitingParker) Park(ctx context.Context, _ ports.ParkedEvent) error {
	if p.attempts.Add(1) == 1 {
		return errors.New("парковка временно недоступна")
	}
	p.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.release:
		p.durable.Store(true)
		return nil
	}
}

func (s *parallelSource) Run(ctx context.Context, consume func(context.Context) error) error {
	if s.run != nil {
		return s.run(ctx, consume)
	}
	return consume(ctx)
}

func (s *parallelSource) Fetch(ctx context.Context) (kafkago.Message, error) {
	select {
	case message := <-s.messages:
		return message, nil
	case <-ctx.Done():
		return kafkago.Message{}, ctx.Err()
	}
}

func (s *parallelSource) Release(context.Context, kafkago.Message) {}

func (s *parallelSource) Commit(ctx context.Context, messages ...kafkago.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.commits <- messages
	return nil
}

func (p controlledProjection) ApplyProduct(ctx context.Context, event appcatalog.ProductEvent) error {
	return p.product(ctx, event)
}

func (p controlledProjection) ApplyPrice(ctx context.Context, event appcatalog.PriceEvent) error {
	return p.price(ctx, event)
}

func bufferedSource(messages ...kafkago.Message) *parallelSource {
	source := &parallelSource{
		messages: make(chan kafkago.Message, len(messages)),
		commits:  make(chan []kafkago.Message, 10),
	}
	for _, message := range messages {
		source.messages <- message
	}
	return source
}

func productMessage(product string, revision, offset int64) kafkago.Message {
	key, _ := json.Marshal([]string{"t1", product})
	return kafkago.Message{
		Topic:     "shop." + kafka.TopicProductProjected,
		Partition: 2,
		Offset:    offset,
		Time:      published,
		Key:       key,
		Value: []byte(fmt.Sprintf(
			`{"version":1,"tenantId":"t1","productId":%q,"revision":%d,"operation":"delete"}`,
			product,
			revision,
		)),
	}
}

func keyedPrice(product string, offset int64) kafkago.Message {
	message := priceMessage(offset, tenantHeader("t1"))
	message.Value = []byte(fmt.Sprintf(`{"id":%q,"price":10}`, product))
	return message
}

func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("ожидание обработки истекло")
		var zero T
		return zero
	}
}

func runParallel(
	t *testing.T,
	source *parallelSource,
	projections kafka.Projections,
) (context.CancelFunc, <-chan error) {
	t.Helper()
	consumer, _ := consumer(source, projections, &parker{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
	})
	return cancel, done
}

func TestConsumerOrdersProductKeyWhileOtherProductRuns(t *testing.T) {
	source := bufferedSource(productMessage("p1", 1, 0), productMessage("p1", 2, 1), productMessage("p2", 1, 2))
	started := make(chan struct{}, 1)
	other := make(chan struct{}, 1)
	release := make(chan struct{})
	processed := make(chan struct{}, 3)
	var mu sync.Mutex
	var revisions []int64
	active := 0
	overlapped := false
	projection := controlledProjection{product: func(ctx context.Context, event appcatalog.ProductEvent) error {
		if event.Snapshot.ProductID == "p2" {
			other <- struct{}{}
		} else {
			mu.Lock()
			overlapped = overlapped || active != 0
			active++
			revisions = append(revisions, event.Snapshot.Revision)
			mu.Unlock()
			if event.Snapshot.Revision == 1 {
				started <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			mu.Lock()
			active--
			mu.Unlock()
		}
		processed <- struct{}{}
		return nil
	}}
	cancel, done := runParallel(t, source, projection)
	await(t, started)
	await(t, other)
	close(release)
	for range 3 {
		await(t, processed)
	}
	cancel()
	if err := await(t, done); err != nil {
		t.Fatal(err)
	}
	if overlapped || len(revisions) != 2 || revisions[0] != 1 || revisions[1] != 2 {
		t.Fatalf("порядок одного товара нарушен: %v, одновременно: %v", revisions, overlapped)
	}
}

func TestConsumerErrorBlocksKeyAndPrefixWhileOtherKeyCompletes(t *testing.T) {
	source := bufferedSource(keyedPrice("p0", 0), keyedPrice("p1", 1), keyedPrice("p2", 2), keyedPrice("p1", 3))
	failed := make(chan struct{}, 1)
	other := make(chan struct{}, 1)
	release := make(chan struct{})
	var attempts atomic.Int32
	var overtook atomic.Bool
	projection := controlledProjection{price: func(ctx context.Context, event appcatalog.PriceEvent) error {
		switch event.EventID {
		case "shop.catalog.product.price-updated:2:1":
			if attempts.Add(1) == 1 {
				failed <- struct{}{}
				return errors.New("временный сбой")
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		case "shop.catalog.product.price-updated:2:2":
			other <- struct{}{}
		case "shop.catalog.product.price-updated:2:3":
			select {
			case <-release:
			default:
				overtook.Store(true)
				return kafka.ErrInvalidPayload.WithMessage("следующее событие обогнало повтор")
			}
		}
		return nil
	}}
	cancel, done := runParallel(t, source, projection)
	await(t, failed)
	await(t, other)
	prefix := await(t, source.commits)
	if len(prefix) != 1 || prefix[0].Offset != 0 {
		t.Fatalf("коммит поверх ошибки в середине партиции: %v", prefix)
	}
	close(release)
	prefix = await(t, source.commits)
	if overtook.Load() {
		t.Fatal("следующее сообщение товара обогнало повтор")
	}
	if len(prefix) != 1 || prefix[0].Offset != 3 {
		t.Fatalf("префикс не продолжился после повтора: %v", prefix)
	}
	cancel()
	if err := await(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestConsumerRevocationAndStopReplayCommittedDatabaseWork(t *testing.T) {
	for _, rebalance := range []bool{false, true} {
		t.Run(fmt.Sprintf("rebalance=%v", rebalance), func(t *testing.T) {
			testConsumerReplay(t, rebalance)
		})
	}
}

func testConsumerReplay(t *testing.T, rebalance bool) {
	t.Helper()
	messages := []kafkago.Message{keyedPrice("p1", 0), keyedPrice("p2", 1), keyedPrice("p1", 2)}
	source := bufferedSource(messages...)
	store := fake.NewStore()
	projector := appcatalog.NewProjector(store, fake.NewClock(published), nil)
	durable := make(chan struct{}, 2)
	firstStopped := make(chan struct{})
	revoke := make(chan context.CancelFunc, 1)
	var replay atomic.Bool
	projection := controlledProjection{price: func(ctx context.Context, event appcatalog.PriceEvent) error {
		if err := projector.ApplyPrice(ctx, event); err != nil {
			return err
		}
		if !replay.Load() {
			durable <- struct{}{}
			if event.ProductID == "p1" {
				<-ctx.Done()
				return ctx.Err()
			}
		}
		return nil
	}}
	source.run = func(ctx context.Context, consume func(context.Context) error) error {
		generation, cancel := context.WithCancel(ctx)
		defer cancel()
		revoke <- cancel
		if err := consume(generation); err != nil {
			return err
		}
		close(firstStopped)
		if rebalance {
			replay.Store(true)
			for _, message := range messages {
				source.messages <- message
			}
			return consume(ctx)
		}
		return nil
	}
	cancel, done := runParallel(t, source, projection)
	await(t, durable)
	await(t, durable)
	if store.Processed("shop.catalog.product.price-updated:2:2") {
		t.Fatal("очередь ключа обогнала незавершённое сообщение")
	}
	if rebalance {
		await(t, revoke)()
	} else {
		cancel()
	}
	await(t, firstStopped)
	if !rebalance {
		if err := await(t, done); err != nil {
			t.Fatal(err)
		}
		select {
		case commits := <-source.commits:
			t.Fatalf("остановка подтвердила сообщение без завершённого итога: %v", commits)
		default:
		}
		// Новый процесс получает те же позиции после сбоя между COMMIT БД и Kafka.
		source = bufferedSource(messages...)
		replay.Store(true)
		cancel, done = runParallel(t, source, projection)
	}
	prefix := await(t, source.commits)
	if len(prefix) != 1 || prefix[0].Offset != 2 {
		t.Fatalf("повторная доставка не завершила префикс: %v", prefix)
	}
	cancel()
	if err := await(t, done); err != nil {
		t.Fatal(err)
	}
	if !store.Processed("shop.catalog.product.price-updated:2:2") || len(store.OutboxRows()) != 2 {
		t.Fatal("повтор потерял очередь ключа или продублировал outbox")
	}
	for _, product := range []string{"p1", "p2"} {
		signals, ok := store.Signals("t1", ids.ProductID(product))
		if !ok || signals.Version != 1 {
			t.Fatalf("повтор применился дважды: %+v", signals)
		}
	}
	if len(store.OutboxRows()) != 2 || !store.Processed("shop.catalog.product.price-updated:2:0") ||
		!store.Processed("shop.catalog.product.price-updated:2:1") {
		t.Fatal("повтор потерял inbox или продублировал outbox")
	}
}

func TestConsumerWaitsForDurableParkingBeforeReleasingKey(t *testing.T) {
	source := bufferedSource(keyedPrice("p1", 0), keyedPrice("p1", 1), keyedPrice("p2", 2))
	parking := &waitingParker{entered: make(chan struct{}, 1), release: make(chan struct{})}
	other := make(chan struct{}, 1)
	var overtook atomic.Bool
	projection := controlledProjection{price: func(_ context.Context, event appcatalog.PriceEvent) error {
		switch event.EventID {
		case "shop.catalog.product.price-updated:2:0":
			return kafka.ErrInvalidPayload
		case "shop.catalog.product.price-updated:2:1":
			overtook.Store(!parking.durable.Load())
		case "shop.catalog.product.price-updated:2:2":
			other <- struct{}{}
		}
		return nil
	}}
	consumer, _ := consumer(source, projection, parking, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	await(t, parking.entered)
	await(t, other)
	select {
	case prefix := <-source.commits:
		t.Fatalf("незаписанная парковка подтверждена: %v", prefix)
	default:
	}
	close(parking.release)
	prefix := await(t, source.commits)
	if len(prefix) != 1 || prefix[0].Offset != 2 || overtook.Load() || parking.attempts.Load() != 2 {
		t.Fatalf("очередь не дождалась парковки: %v, попыток %d", prefix, parking.attempts.Load())
	}
	cancel()
	if err := await(t, done); err != nil {
		t.Fatal(err)
	}
}
