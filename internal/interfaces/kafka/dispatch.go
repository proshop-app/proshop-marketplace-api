package kafka

import (
	"context"
	"strings"
	"sync"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/pkg/safego"
)

// projectionBatch — сколько снимков разных товаров воркер применяет одной транзакцией. Окно
// чтения партиции — 64 сообщения, на реплику приходится несколько партиций: пачка набирается
// и при волне одного магазина, а транзакция остаётся короткой.
const projectionBatch = 50

// lane — каналы раздачи: воркер объявляет готовность, раздатчик отдаёт ему пачку.
type lane struct {
	idle      chan struct{}
	jobs      chan []*delivery
	completed chan handled
}

func (c *Consumer) runGeneration(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	var running sync.WaitGroup
	defer cancel()

	incoming := make(chan kafkago.Message)
	work := lane{
		idle: make(chan struct{}),
		// Раздатчик кладёт пачку только после готовности воркера, поэтому отправка не ждёт.
		jobs:      make(chan []*delivery, c.config.Workers),
		completed: make(chan handled, c.config.Workers),
	}
	c.startWorkers(ctx, cancel, &running, work)
	running.Add(1)
	// Паника чтения или воркера вне обработчика сообщения останавливает поколение:
	// незавершённое сообщение не фиксируется и придёт повторно, а не застрянет.
	safego.Run(
		"kafka.fetch",
		func() error {
			defer running.Done()
			c.fetchMessages(ctx, incoming)
			return nil
		},
		stopOnPanic(cancel),
	)

	commits := &offsetCommitter{progress: make(offsetProgress), commit: c.source.Commit, logger: c.logger}
	c.dispatch(ctx, incoming, work, commits)
	cancel()
	running.Wait()
	commits.finish(parent, c.source, work.completed)
	return nil
}

func (c *Consumer) dispatch(ctx context.Context, incoming <-chan kafkago.Message, work lane, commits *offsetCommitter) {
	queue := newKeyQueue()
	var retries retrySchedule
	defer retries.stop()
	ticker := time.NewTicker(commitInterval)
	defer ticker.Stop()
	// idle — воркеры, ждущие задание; busy — выданные задания. Ожидающие делятся на все незанятые
	// воркеры: пачка не забирает соседей у воркеров, которые ещё не успели объявить готовность.
	idle, busy := 0, 0

	for ctx.Err() == nil {
		for ; idle > 0; idle-- {
			batch := queue.take(projectionBatch, max(1, c.config.Workers-busy), c.batchable)
			if len(batch) == 0 {
				break
			}
			work.jobs <- batch
			busy++
		}
		select {
		case <-ctx.Done():
			return
		case message := <-incoming:
			queue.add(&delivery{
				message: message,
				key:     messageKey(message, c.config.TopicPrefix),
				offset:  commits.progress.add(message),
			})
		case <-work.idle:
			idle++
		case result := <-work.completed:
			busy--
			for _, item := range result.done {
				queue.finished(item)
				item.offset.complete()
				c.source.Release(ctx, item.message)
			}
			// Пока повтор ждёт срока, ключ остаётся занят, а воркер принимает другие сообщения.
			for _, item := range result.retry {
				retries.deferRetry(queue, item, time.Now())
			}
			commits.flush(ctx, false)
		case <-retries.wakeup():
			retries.release(queue, time.Now())
		case <-ticker.C:
			commits.flush(ctx, true)
		}
	}
}

// batchable — сообщение идёт в пачку снимков товаров; остальные виды и повтор после отката
// пачки разбираются по одному.
func (c *Consumer) batchable(item *delivery) bool {
	return !item.alone && c.router.Batchable(strings.TrimPrefix(item.message.Topic, prefixOf(c.config.TopicPrefix)))
}

func (c *Consumer) fetchMessages(ctx context.Context, incoming chan<- kafkago.Message) {
	for ctx.Err() == nil {
		message, ok := c.next(ctx)
		if !ok {
			return
		}
		select {
		case incoming <- message:
		case <-ctx.Done():
			return
		}
	}
}

func (c *Consumer) startWorkers(ctx context.Context, cancel context.CancelFunc, running *sync.WaitGroup, work lane) {
	for range c.config.Workers {
		running.Add(1)
		safego.Run(
			"kafka.worker",
			func() error {
				defer running.Done()
				c.work(ctx, work)
				return nil
			},
			stopOnPanic(cancel),
		)
	}
}

// stopOnPanic останавливает поколение, если горутина приёма упала паникой.
func stopOnPanic(cancel context.CancelFunc) func(error) {
	return func(err error) {
		if err != nil {
			cancel()
		}
	}
}

func (c *Consumer) work(ctx context.Context, work lane) {
	for ctx.Err() == nil {
		select {
		case work.idle <- struct{}{}:
		case <-ctx.Done():
			return
		}
		var batch []*delivery
		select {
		case batch = <-work.jobs:
		case <-ctx.Done():
			return
		}
		result, err := c.processBatch(ctx, batch)
		if err != nil {
			return
		}
		select {
		case work.completed <- result:
		case <-ctx.Done():
			return
		}
	}
}
