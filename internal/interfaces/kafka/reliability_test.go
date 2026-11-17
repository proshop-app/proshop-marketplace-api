package kafka_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	kafkago "github.com/segmentio/kafka-go"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

func TestConsumerCountsUnknownFailuresSeparatelyFromDependencyAndConflict(t *testing.T) {
	calls := 0
	projection := controlledProjection{price: func(context.Context, appcatalog.PriceEvent) error {
		calls++
		switch {
		case calls == 1 || calls == 18 || calls == 35:
			return errs.New(errs.Internal, "test.unknown", "неизвестный отказ")
		case calls < 18:
			return errs.New(errs.Unavailable, "test.dependency", "зависимость недоступна")
		default:
			return errs.New(errs.Conflict, "test.version", "конкурентное изменение")
		}
	}}
	parked := &parker{}
	consumer, _ := consumer(&source{}, projection, parked, nil)
	if err := consumer.Process(context.Background(), priceMessage(1, tenantHeader("t1"))); err != nil {
		t.Fatal(err)
	}
	if calls != 35 || len(parked.events) != 1 || parked.events[0].Reason != kafka.ReasonExhausted ||
		parked.events[0].Attempts != 35 {
		t.Fatalf("предел должен учитывать только три неизвестных отказа: %d, %+v", calls, parked.events)
	}
}

func TestConsumerDependencyRetryKeepsTenantIsolationOrderAndPrefix(t *testing.T) {
	otherMessage := keyedPrice("p2", 2)
	otherMessage.Headers = []kafkago.Header{tenantHeader("t2")}
	source := bufferedSource(keyedPrice("p1", 0), keyedPrice("p1", 1), otherMessage)
	other := make(chan struct{}, 1)
	pastBudget := make(chan struct{}, 1)
	restore := make(chan struct{})
	var attempts atomic.Int32
	var recovered, overtook atomic.Bool
	projection := controlledProjection{price: func(_ context.Context, event appcatalog.PriceEvent) error {
		switch event.EventID {
		case "shop.catalog.product.price-updated:2:0":
			select {
			case <-restore:
				recovered.Store(true)
				return nil
			default:
				if attempts.Add(1) == 12 {
					pastBudget <- struct{}{}
				}
				return errs.New(errs.Unavailable, "test.dependency", "зависимость недоступна")
			}
		case "shop.catalog.product.price-updated:2:1":
			overtook.Store(!recovered.Load())
		case "shop.catalog.product.price-updated:2:2":
			other <- struct{}{}
		}
		return nil
	}}
	parked := &parker{}
	consumer, _ := consumer(source, projection, parked, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	await(t, other)
	await(t, pastBudget)
	select {
	case prefix := <-source.commits:
		t.Fatalf("отказ зависимости пропущен при фиксации префикса: %v", prefix)
	default:
	}
	close(restore)
	prefix := await(t, source.commits)
	cancel()
	if err := await(t, done); err != nil {
		t.Fatal(err)
	}
	if len(parked.events) != 0 || overtook.Load() || len(prefix) != 1 || prefix[0].Offset != 2 {
		t.Fatalf("потеря повтора, порядка или префикса: %+v, %v, %v", parked.events, overtook.Load(), prefix)
	}
}

func TestConsumerDoesNotParkCancelledPermanentOrExhaustedWork(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		projection := controlledProjection{price: func(context.Context, appcatalog.PriceEvent) error {
			calls++
			if permanent {
				cancel()
				return kafka.ErrInvalidPayload
			}
			if calls == 3 {
				cancel()
			}
			return errs.New(errs.Internal, "test.unknown", "неизвестный отказ")
		}}
		parked := &parker{}
		consumer, _ := consumer(&source{}, projection, parked, nil)
		err := consumer.Process(ctx, priceMessage(1, tenantHeader("t1")))
		cancel()
		if !errors.Is(err, context.Canceled) || len(parked.events) != 0 {
			t.Fatalf("отмена процесса стала отказом задачи: %v, %+v", err, parked.events)
		}
	}
}
