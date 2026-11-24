package kafka_test

import (
	"context"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

// panicking падает паникой на цене одного товара, остальные цены принимает.
type panicking struct {
	recorder
}

func (p *panicking) ApplyStock(ctx context.Context, e appcatalog.StockEvent) error {
	if e.ProductID == "p1" {
		panic("сбой разбора снимка")
	}

	return p.recorder.ApplyStock(ctx, e)
}

func TestConsumerParksPanickingMessageAndKeepsOthers(t *testing.T) {
	projections := &panicking{}
	park := &parker{}
	c, registry := consumer(&source{}, projections, park, nil)

	if err := c.Process(context.Background(), stockMessage(7, tenantHeader("t1"))); err != nil {
		t.Fatal(err)
	}
	other := message("p2", `{"productId":"p2","quantity":1}`, tenantHeader("t1"))
	other.Topic = "shop." + kafka.TopicStockItemUpdated
	if err := c.Process(context.Background(), other); err != nil {
		t.Fatal(err)
	}

	if len(park.events) != 1 || park.events[0].Reason != kafka.ReasonExhausted || park.events[0].Offset != 7 ||
		park.events[0].TenantID != "t1" || park.events[0].Attempts != 3 {
		t.Fatalf("паника — неизвестный сбой: повторы, затем отравленные: %+v", park.events)
	}
	if len(projections.stock) != 1 || projections.stock[0].ProductID != "p2" {
		t.Fatalf("следующее сообщение обработано: %+v", projections.stock)
	}
	if got := sum(t, registry, "marketplace_kafka_handler_panics_total"); got != 3 {
		t.Fatalf("метрика паник по попыткам: %v", got)
	}
}
