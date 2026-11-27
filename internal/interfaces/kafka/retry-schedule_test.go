package kafka

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func TestRetryDeadlineKeepsOrderAndLeavesOtherTenantRunnable(t *testing.T) {
	now := time.Unix(100, 0)
	queue := newKeyQueue()
	progress := make(offsetProgress)
	items := []*delivery{
		{key: orderingKey{kind: orderTenant, tenant: "t1"}},
		{key: orderingKey{kind: orderProduct, tenant: "t1", entity: "p1"}},
		{key: orderingKey{kind: orderTenant, tenant: "t2"}},
	}
	for i, item := range items {
		item.offset = progress.add(kafkago.Message{Topic: TopicTenantCreated, Offset: int64(i)})
	}
	queue.add(items[0])
	take := func() *delivery {
		batch := queue.take(1, 1, func(*delivery) bool { return false })
		if len(batch) == 0 {
			return nil
		}
		return batch[0]
	}
	if got := take(); got != items[0] {
		t.Fatal("первое событие не выдано")
	}
	items[0].retry.delay = time.Minute
	var retry retrySchedule
	retry.deferRetry(queue, items[0], now)
	queue.add(items[1])
	queue.add(items[2])
	if got := take(); got != items[2] {
		t.Fatal("смена реестра задержала чужой магазин")
	}
	queue.finished(items[2])
	items[2].offset.complete()
	retry.release(queue, now)
	if take() != nil || progress.count() != 0 {
		t.Fatal("до срока изменились порядок или префикс коммита")
	}
	retry.release(queue, now.Add(time.Minute))
	if got := take(); got != items[0] || !got.alone {
		t.Fatal("по сроку нужна отдельная попытка того же сообщения")
	}
	queue.finished(items[0])
	items[0].offset.complete()
	if got := take(); got != items[1] {
		t.Fatal("следующее событие магазина не выдано после итога")
	}
	queue.finished(items[1])
	items[1].offset.complete()
	messages, count := progress.batch()
	if count != 3 || len(messages) != 1 || messages[0].Offset != 2 {
		t.Fatalf("позиции: %v, %d", messages, count)
	}
}

func TestWorkerReturnsRetryAfterOneAttempt(t *testing.T) {
	clock := fake.NewClock(time.Unix(100, 0))
	calls := 0
	router := &Router{handlers: map[string]handler{
		TopicTenantCreated: func(context.Context, Envelope) error {
			calls++
			if calls == 1 {
				return participant.ErrTenantConflict
			}
			return nil
		},
	}}
	c := NewConsumer(
		nil,
		router,
		nil,
		ConsumerConfig{
			Backoff: func(int) time.Duration { return 0 },
			Now:     clock.Now,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewConsumerMetrics(prometheus.NewRegistry()),
	)
	item := &delivery{message: kafkago.Message{Topic: TopicTenantCreated}}
	first, err := c.processBatch(context.Background(), []*delivery{item})
	if err != nil || calls != 1 || len(first.done) != 0 || len(first.retry) != 1 || first.retry[0] != item {
		t.Fatalf("воркер должен вернуть повтор после одной попытки: %d, %+v, %v", calls, first, err)
	}
	second, err := c.processBatch(context.Background(), first.retry)
	if err != nil || calls != 2 || len(second.done) != 1 || len(second.retry) != 0 {
		t.Fatalf("результат следующей попытки: %d, %+v, %v", calls, second, err)
	}
}
