package kafka

import (
	"context"
	"fmt"
	"strings"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// Replay — повтор через тот же разбор, без публикации в чужие топики.
type Replay struct {
	router *Router
	prefix string
}

// NewReplay связывает outbox с обработчиком исходных конвертов.
func NewReplay(router *Router, prefix string) *Replay {
	return &Replay{router: router, prefix: prefixOf(prefix)}
}

// Replay сохраняет позицию, заголовки и время исходного сообщения.
func (r *Replay) Replay(ctx context.Context, event ports.ParkedEvent) error {
	if event.PublishedAt.IsZero() || event.PublishedAt.Equal(time.Unix(0, 0)) {
		return ErrInvalidPayload.WithMessage("нет исходного времени события, нужен новый снимок источника")
	}
	headers := make([]kafkago.Header, 0, len(event.Headers))
	for key, value := range event.Headers {
		headers = append(headers, kafkago.Header{Key: key, Value: []byte(value)})
	}
	return r.router.Handle(ctx, Envelope{
		Topic:   strings.TrimPrefix(event.Topic, r.prefix),
		EventID: fmt.Sprintf("%s:%d:%d", event.Topic, event.Partition, event.Offset),
		Message: kafkago.Message{
			Topic:     event.Topic,
			Partition: event.Partition,
			Offset:    event.Offset,
			Key:       event.Key,
			Value:     event.Value,
			Headers:   headers,
			Time:      event.PublishedAt,
		},
	})
}
