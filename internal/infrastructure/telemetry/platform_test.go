package telemetry

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestPlatformOutcomesAndDuration(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewPlatform(registry)
	outcomes := []string{"ok", "timeout", "error", "short_circuit", "stale"}
	for _, outcome := range outcomes {
		metrics.PlatformCall("reviews", outcome, time.Second)
	}
	families, err := registry.Gather()
	if err != nil || len(families) != 2 {
		t.Fatal(families, err)
	}
	for _, family := range families {
		if len(family.Metric) != len(outcomes) {
			t.Fatal(family)
		}
		for _, metric := range family.Metric {
			if metric.Counter != nil && metric.Counter.GetValue() != 1 {
				t.Fatal(metric)
			}
			if metric.Histogram != nil &&
				(metric.Histogram.GetSampleCount() != 1 || metric.Histogram.GetSampleSum() != 1) {
				t.Fatal(metric)
			}
		}
	}
}
