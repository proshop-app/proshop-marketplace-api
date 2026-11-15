package kafka

import (
	"context"
	"os"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/pricecontexts"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestPriceContextContractInvalidatesAndParksInvalid(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/api/examples/price-context-changed.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	buyer := ids.UUID("00000000-0000-4000-8000-000000000001")
	cache := &fake.PriceContextCache{}
	clock := fake.NewClock(time.Now())
	service := pricecontexts.New(ctx, pricecontexts.Config{}, fake.NewStore(), nil, cache, clock, &fake.IDs{}, nil)
	if err := cache.Begin(ctx, buyer, "seed", time.Minute); err != nil {
		t.Fatal(err)
	}
	_, err = cache.Store(ctx, buyer, "seed", ports.PriceContextEntry{LoadedAt: clock.Now()}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(nil, nil).WithPriceContexts(service)
	envelope := Envelope{
		Topic:   TopicPriceContextChanged,
		EventID: "pricing.buyer.context-changed:0:1",
		Message: kafkago.Message{
			Key:   []byte(`["000000000000000000000001","00000000-0000-4000-8000-000000000001"]`),
			Value: raw,
		},
	}
	if err := router.Handle(ctx, envelope); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := cache.Get(ctx, buyer); found {
		t.Fatal("событие не удалило запись")
	}
	if err := router.Handle(ctx, envelope); err != nil {
		t.Fatal("повтор события", err)
	}
	envelope.Message.Headers = []kafkago.Header{{Key: HeaderTenant, Value: []byte("t2")}}
	if err := router.Handle(ctx, envelope); errs.CodeOf(err) != errs.Invalid {
		t.Fatal("расхождение магазина принято", err)
	}
	envelope.Message.Headers = nil
	envelope.Message.Key = []byte(`["000000000000000000000001","other"]`)
	if err := router.Handle(ctx, envelope); errs.CodeOf(err) != errs.Invalid {
		t.Fatal("расхождение покупателя принято", err)
	}
}
