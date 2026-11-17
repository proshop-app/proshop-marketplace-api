package kafka

import (
	"context"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

const (
	commitBatchSize = 100
	commitInterval  = time.Second
)

// Сбой коммита оставляет весь префикс на повтор; обработка других ключей продолжается.
type offsetCommitter struct {
	progress offsetProgress
	commit   func(context.Context, ...kafkago.Message) error
	logger   *slog.Logger
	retrying bool
}

func (c *offsetCommitter) flush(ctx context.Context, timed bool) {
	count := c.progress.count()
	if ctx.Err() != nil || count == 0 || !timed && (c.retrying || count < commitBatchSize) {
		return
	}
	messages, _ := c.progress.batch()
	if err := c.commit(ctx, messages...); err != nil {
		c.retrying = true
		if ctx.Err() == nil {
			c.logger.Warn("kafka: коммит пачки не удался, повтор через секунду", slog.Any("error", err))
		}
		return
	}
	c.progress.committed()
	c.retrying = false
}
