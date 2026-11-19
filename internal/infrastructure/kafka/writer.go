package kafka

import (
	"context"
	"errors"
	"fmt"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// ErrUnavailable — сетевой отказ брокера допускает повтор исходного намерения.
var ErrUnavailable = errs.New(errs.Unavailable, "kafka.unavailable", "брокер недоступен")

// writerBatchTimeout — сколько писатель ждёт попутчиков; событие реестра не должно ждать секунду.
const writerBatchTimeout = 50 * time.Millisecond

// Writer — публикация событий площадки. Топик не заводится: им владеет сервер
// установки, отсутствующий топик — ошибка, и задача outbox уходит на повтор.
// Ключ сообщения выбирает партицию: события одного ключа идут по порядку.
type Writer struct {
	writer messageWriter
}

type messageWriter interface {
	WriteMessages(context.Context, ...kafkago.Message) error
	Close() error
}

// NewWriter — писатель с подтверждением всех реплик.
func NewWriter(brokers []string) *Writer {
	return &Writer{writer: &kafkago.Writer{
		Addr:                   kafkago.TCP(brokers...),
		Balancer:               &kafkago.Hash{},
		RequiredAcks:           kafkago.RequireAll,
		AllowAutoTopicCreation: false,
		BatchTimeout:           writerBatchTimeout,
	}}
}

// Publish пишет сообщение и ждёт подтверждения брокера.
func (w *Writer) Publish(ctx context.Context, message ports.Message) error {
	failures := w.PublishBatch(ctx, []ports.Message{message})
	return failures[0]
}

// PublishBatch делает один вызов Kafka; WriteErrors сопоставлены исходным сообщениям.
func (w *Writer) PublishBatch(ctx context.Context, messages []ports.Message) []error {
	batch := make([]kafkago.Message, 0, len(messages))
	for _, message := range messages {
		headers := make([]kafkago.Header, 0, len(message.Headers))
		for key, value := range message.Headers {
			headers = append(headers, kafkago.Header{Key: key, Value: []byte(value)})
		}
		batch = append(batch, kafkago.Message{
			Topic:   message.Topic,
			Key:     message.Key,
			Value:   message.Value,
			Headers: headers,
		})
	}
	if len(batch) == 0 {
		return nil
	}
	return publicationFailures(w.writer.WriteMessages(ctx, batch...), len(batch))
}

func publicationFailures(err error, count int) []error {
	failures := make([]error, count)
	var individual kafkago.WriteErrors
	partial := errors.As(err, &individual) && len(individual) == count
	for n := range failures {
		cause := err
		if partial {
			cause = individual[n]
		}
		if cause != nil {
			failures[n] = ErrUnavailable.Wrap(fmt.Errorf("публикация: %w", cause))
		}
	}
	return failures
}

// Close дожидается отправки и закрывает соединения.
func (w *Writer) Close() error {
	return w.writer.Close()
}
