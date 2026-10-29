package kafka

import (
	"context"
	"fmt"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// writerBatchTimeout — сколько писатель ждёт попутчиков; событие реестра не должно ждать секунду.
const writerBatchTimeout = 50 * time.Millisecond

// Writer — публикация событий площадки. Топик не заводится: им владеет сервер
// установки, отсутствующий топик — ошибка, и задача outbox уходит на повтор.
// Ключ сообщения выбирает партицию: события одного ключа идут по порядку.
type Writer struct {
	writer *kafkago.Writer
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
	headers := make([]kafkago.Header, 0, len(message.Headers))
	for key, value := range message.Headers {
		headers = append(headers, kafkago.Header{Key: key, Value: []byte(value)})
	}
	err := w.writer.WriteMessages(ctx, kafkago.Message{
		Topic:   message.Topic,
		Key:     message.Key,
		Value:   message.Value,
		Headers: headers,
	})
	if err != nil {
		return fmt.Errorf("kafka: публикация в %s: %w", message.Topic, err)
	}
	return nil
}

// Close дожидается отправки и закрывает соединения.
func (w *Writer) Close() error {
	return w.writer.Close()
}
