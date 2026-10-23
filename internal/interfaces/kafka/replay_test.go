package kafka_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

func TestParkedReplayKeepsOriginalIdentityAndTime(t *testing.T) {
	rec := &recorder{}
	replay := kafka.NewReplay(kafka.NewRouter(rec, rec), "cell")
	value := ports.ParkedEvent{
		Topic:       "cell." + kafka.TopicProductProjected,
		Partition:   2,
		Offset:      17,
		Key:         []byte(`["t1","p1"]`),
		Headers:     map[string]string{"x-tenant": "t1"},
		Value:       []byte(productBody),
		PublishedAt: published,
	}
	if err := replay.Replay(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if len(rec.products) != 1 || rec.products[0].EventID != value.Topic+":2:17" ||
		!rec.products[0].Snapshot.At.Equal(published) {
		t.Fatal(rec.products)
	}
	value.PublishedAt = time.Time{}
	if err := replay.Replay(context.Background(), value); !errors.Is(err, kafka.ErrInvalidPayload) {
		t.Fatal(err)
	}
}
