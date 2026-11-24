package kafka_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	kafkago "github.com/segmentio/kafka-go"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
)

func TestConsumerFinalPrefixOnShutdownAndNoCommitOnRevoke(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint("revoke=", revoke), func(t *testing.T) {
			source := bufferedSource(keyedStock("p1", 0, 1), keyedStock("p2", 1, 1), keyedStock("p3", 2, 1))
			generation, cancelGeneration := context.WithCancel(context.Background())
			defer cancelGeneration()
			source.generation = generation
			source.released = make(chan kafkago.Message, 3)
			source.commit = func(ctx context.Context, messages ...kafkago.Message) error {
				// Даже на медленном CI обычный таймер не должен забрать проверяемый префикс.
				if _, final := ctx.Deadline(); !final {
					return errors.New("обычная фиксация временно недоступна")
				}
				source.commits <- messages
				return nil
			}
			source.run = func(parent context.Context, consume func(context.Context) error) error {
				ctx, cancel := context.WithCancel(parent)
				defer cancel()
				stop := context.AfterFunc(generation, cancel)
				defer stop()
				return consume(ctx)
			}
			blocked := make(chan struct{}, 1)
			projection := controlledProjection{stock: func(ctx context.Context, event appcatalog.StockEvent) error {
				if event.ProductID == "p2" {
					blocked <- struct{}{}
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}}
			cancel, done := runParallel(t, source, projection)
			await(t, blocked)
			await(t, source.released)
			await(t, source.released)
			if revoke {
				cancelGeneration()
			} else {
				cancel()
			}
			if err := await(t, done); err != nil {
				t.Fatal(err)
			}
			select {
			case prefix := <-source.commits:
				if revoke || len(prefix) != 1 || prefix[0].Offset != 0 {
					t.Fatalf("неверная финальная фиксация: %v", prefix)
				}
			default:
				if !revoke {
					t.Fatal("штатная остановка не зафиксировала завершённый префикс")
				}
			}
		})
	}
}
