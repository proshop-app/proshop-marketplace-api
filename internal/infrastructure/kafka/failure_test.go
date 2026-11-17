package kafka

import (
	"context"
	"errors"
	"io"
	"testing"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

func TestPublishCancellationRemainsRetryable(t *testing.T) {
	writer := NewWriter([]string{"broker.invalid:9092"})
	defer func() { _ = writer.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := writer.Publish(ctx, ports.Message{Topic: "test", Value: []byte("{}")})
	if errs.CodeOf(err) != errs.Unavailable || !errors.Is(err, context.Canceled) {
		t.Fatal("сбой брокера должен сохранять класс и причину", err)
	}
}

func TestGenerationNetworkFailuresRemainUnavailable(t *testing.T) {
	session := &readSession{
		generation: context.Background(),
		messages:   make(chan fetchResult, 1),
		commit: func(context.Context, map[string]map[int]int64) error {
			return io.EOF
		},
	}
	session.messages <- fetchResult{err: io.EOF}
	ctx := context.WithValue(context.Background(), generationKey{}, session)
	reader := NewReader(Config{})
	_, fetchErr := reader.Fetch(ctx)
	commitErr := reader.Commit(ctx, kafkago.Message{Topic: "test", Offset: 1})
	for _, err := range []error{fetchErr, commitErr} {
		if errs.CodeOf(err) != errs.Unavailable || !errors.Is(err, io.EOF) {
			t.Fatalf("сбой поколения потерял класс зависимости или причину: %v", err)
		}
	}
}
