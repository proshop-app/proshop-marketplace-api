package telemetry_test

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/telemetry"
)

func TestOfferBatchMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	observer := telemetry.NewOfferBatches(registry)
	observer.ObserveBatch(100, time.Second, nil)
	observer.ObserveBatch(20, 2*time.Second, errors.New("сбой"))
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		count, sum := uint64(0), 0.0
		for _, metric := range family.GetMetric() {
			count += metric.GetHistogram().GetSampleCount()
			sum += metric.GetHistogram().GetSampleSum()
		}
		if count != 2 {
			t.Fatalf("пропущена отправка: %s", family.GetName())
		}
		if family.GetName() == "marketplace_offer_index_batch_size" && sum != 120 {
			t.Fatalf("размер пачек: %v", sum)
		}
		if family.GetName() == "marketplace_offer_index_send_seconds" && sum != 3 {
			t.Fatalf("время отправки: %v", sum)
		}
	}
}
