//go:build integration

package kafka_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	infrakafka "github.com/proshop-app/proshop-marketplace/internal/infrastructure/kafka"
)

func TestWriterModerationStatuses(t *testing.T) {
	at := time.Now().UTC().Format(time.RFC3339)
	cases := []struct {
		name string
		key  string
		body any
	}{
		{
			name: "marketplace.offer.status",
			key:  `["t1","p1"]`,
			body: moderationstatus.OfferEvent{
				SchemaVersion: 1,
				TenantID:      "t1",
				ProductID:     "p1",
				Revision:      12,
				Status:        "published",
				Visible:       true,
				Reasons:       []moderationstatus.Reason{},
				At:            at,
			},
		},
		{
			name: "marketplace.participant.status",
			key:  "t1",
			body: moderationstatus.ParticipantEvent{
				SchemaVersion: 1,
				TenantID:      "t1",
				Enabled:       true,
				Trust:         "trusted",
				By:            "system",
				At:            at,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topic := unique("p1." + tc.name + ".")
			createTopics(t, topic)
			writer := infrakafka.NewWriter(brokers())
			t.Cleanup(func() { _ = writer.Close() })
			value, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			message := ports.Message{
				Topic:   topic,
				Key:     []byte(tc.key),
				Value:   value,
				Headers: map[string]string{"x-tenant": "t1"},
			}
			for range 2 {
				for {
					err := writer.Publish(ctx, message)
					if err == nil {
						break
					}
					if ctx.Err() != nil {
						t.Fatal(err)
					}
					time.Sleep(500 * time.Millisecond)
				}
			}
			reader := kafkago.NewReader(kafkago.ReaderConfig{
				Brokers:     brokers(),
				GroupID:     unique("moderation-writer-"),
				Topic:       topic,
				StartOffset: kafkago.FirstOffset,
			})
			t.Cleanup(func() { _ = reader.Close() })
			partition := -1
			for range 2 {
				got, err := reader.ReadMessage(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if got.Topic != topic || string(got.Key) != tc.key || string(got.Value) != string(value) ||
					len(got.Headers) != 1 || got.Headers[0].Key != "x-tenant" || string(got.Headers[0].Value) != "t1" {
					t.Fatalf("сообщение не совпало: %+v", got)
				}
				if partition >= 0 && partition != got.Partition {
					t.Fatal("один ключ попал в разные партиции")
				}
				partition = got.Partition
			}
			missing := unique("p1." + tc.name + ".missing.")
			message.Topic = missing
			missingCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			if err := writer.Publish(missingCtx, message); err == nil || topicExists(t, missing) {
				t.Fatal("писатель не должен создавать топик")
			}
		})
	}
}
