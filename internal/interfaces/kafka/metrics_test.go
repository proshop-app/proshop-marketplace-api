package kafka_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

func TestLagMetricsUseShortTopicAndForgetRevokedPartitions(t *testing.T) {
	registry := prometheus.NewRegistry()
	lag := kafka.NewConsumerMetrics(registry).Lag("shop")
	lag.ObserveLag("shop."+kafka.TopicProductProjected, 3, 42)

	series := lagSeries(t, registry)
	if len(series) != 1 || series[kafka.TopicProductProjected+"/3"] != 42 {
		t.Fatalf("отставание по короткому имени топика и партиции, без метки магазина: %v", series)
	}

	lag.ForgetLag("shop."+kafka.TopicProductProjected, 3)
	if series := lagSeries(t, registry); len(series) != 0 {
		t.Fatalf("партиция, ушедшая из назначения, не висит в метрике: %v", series)
	}
}

func lagSeries(t *testing.T, registry *prometheus.Registry) map[string]float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	series := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "marketplace_kafka_lag_messages" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			series[labels["topic"]+"/"+labels["partition"]] = metric.GetGauge().GetValue()
		}
	}
	return series
}
