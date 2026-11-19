package kafka

import (
	"context"
	"errors"
	"io"
	"testing"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

type batchMessageWriter struct {
	calls    int
	messages []kafkago.Message
}

func (w *batchMessageWriter) WriteMessages(_ context.Context, messages ...kafkago.Message) error {
	w.calls++
	w.messages = messages
	failures := make(kafkago.WriteErrors, len(messages))
	failures[3] = io.ErrUnexpectedEOF
	return failures
}

func (*batchMessageWriter) Close() error { return nil }

func TestPublishBatchUsesOneKafkaCallAndKeepsPartialErrors(t *testing.T) {
	broker := &batchMessageWriter{}
	writer := &Writer{writer: broker}
	messages := make([]ports.Message, 200)
	for n := range messages {
		messages[n] = ports.Message{Topic: "status", Key: []byte("key"), Headers: map[string]string{"x-tenant": "t1"}}
	}
	failures := writer.PublishBatch(context.Background(), messages)
	if broker.calls != 1 || len(broker.messages) != 200 || len(failures) != 200 {
		t.Fatal(broker.calls, len(broker.messages), len(failures))
	}
	for n, failure := range failures {
		if (failure != nil) != (n == 3) {
			t.Fatal(n, failure)
		}
	}
	if !errors.Is(failures[3], io.ErrUnexpectedEOF) || string(broker.messages[0].Headers[0].Value) != "t1" {
		t.Fatal(failures[3], broker.messages[0])
	}
}
