package kafka

import (
	"context"
	"errors"
	"testing"

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
