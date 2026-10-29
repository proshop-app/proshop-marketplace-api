//go:build integration

package kafka_test

import (
	"context"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	infrakafka "github.com/proshop-app/proshop-marketplace/internal/infrastructure/kafka"
)

func TestWriterPublishesByKeyAndNeverCreatesTopics(t *testing.T) {
	topic := unique("catalog.reference.changed.")
	createTopics(t, topic)
	writer := infrakafka.NewWriter(brokers())
	t.Cleanup(func() { _ = writer.Close() })

	deadline := time.Now().Add(30 * time.Second)
	for _, value := range []string{`{"revisionTo":1}`, `{"revisionTo":2}`} {
		headers := map[string]string{"x": "1"}
		message := ports.Message{Topic: topic, Key: []byte("b1"), Value: []byte(value), Headers: headers}
		for {
			err := writer.Publish(context.Background(), message)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("публикация: %v", err)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:     brokers(),
		GroupID:     unique("reference-writer-"),
		Topic:       topic,
		StartOffset: kafkago.FirstOffset,
	})
	t.Cleanup(func() { _ = reader.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var partitions []int
	for i := 0; i < 2; i++ {
		message, err := reader.ReadMessage(ctx)
		if err != nil {
			t.Fatalf("чтение: %v", err)
		}
		if string(message.Key) != "b1" || len(message.Headers) != 1 {
			t.Fatalf("сообщение: %+v", message)
		}
		partitions = append(partitions, message.Partition)
	}
	if partitions[0] != partitions[1] {
		t.Fatalf("события одного словаря в разных партициях: %v", partitions)
	}

	missing := unique("catalog.reference.missing.")
	publishCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	orphan := ports.Message{Topic: missing, Key: []byte("b1"), Value: []byte(`{}`)}
	if err := writer.Publish(publishCtx, orphan); err == nil {
		t.Fatal("публикация в несуществующий топик прошла")
	}
	if topicExists(t, missing) {
		t.Fatal("писатель завёл топик у брокера")
	}
}
