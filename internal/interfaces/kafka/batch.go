package kafka

import (
	"context"
	"log/slog"
	"slices"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// handled — итог задания воркера: done записаны и отпускают позицию и ключ, retry возвращаются
// раздатчику отдельными заданиями и идут обычным путём повторов и отравленных.
type handled struct {
	done  []*delivery
	retry []*delivery
}

// processBatch применяет пачку снимков разных товаров одной транзакцией. Неразобранные сообщения
// и вся пачка после отката не держат соседей: после отката каждое сообщение применяется по
// одному разу, успешные сразу отпускаются, повторяются только неуспешные. Ошибка — только
// отмена контекста.
func (c *Consumer) processBatch(ctx context.Context, batch []*delivery) (handled, error) {
	if len(batch) == 1 {
		return c.attemptEach(ctx, batch)
	}
	envelopes := make([]Envelope, 0, len(batch))
	for _, item := range batch {
		envelopes = append(envelopes, c.envelope(item.message))
	}
	var rejected []int
	err := errs.Guard(func() error {
		var err error
		rejected, err = c.router.HandleProducts(ctx, envelopes)
		return err
	})
	if ctx.Err() != nil {
		return handled{}, ctx.Err()
	}
	if err != nil {
		c.metrics.batchFallbacks.WithLabelValues(envelopes[0].Topic).Inc()
		c.logger.Warn(
			"kafka: пачка снимков откатилась, сообщения по одному",
			slog.Int("messages", len(batch)),
			slog.Any("error", err),
		)
		return c.attemptEach(ctx, batch)
	}
	result := handled{}
	applied := map[string]int{}
	for n, item := range batch {
		if slices.Contains(rejected, n) {
			result.retry = append(result.retry, item)
			continue
		}
		applied[envelopes[n].Topic]++
		result.done = append(result.done, item)
		c.metrics.messages.WithLabelValues(envelopes[n].Topic, "processed").Inc()
	}
	for topic, count := range applied {
		c.metrics.batchSize.WithLabelValues(topic).Observe(float64(count))
	}
	return result, nil
}

// attemptEach применяет сообщения откатившейся пачки по одной попытке: сбой одного не задерживает
// следующих, его повторит отдельное задание.
func (c *Consumer) attemptEach(ctx context.Context, batch []*delivery) (handled, error) {
	result := handled{}
	for _, item := range batch {
		if ctx.Err() != nil {
			return handled{}, ctx.Err()
		}
		if c.attempt(ctx, item) {
			result.done = append(result.done, item)
		} else {
			result.retry = append(result.retry, item)
		}
	}
	return result, ctx.Err()
}
