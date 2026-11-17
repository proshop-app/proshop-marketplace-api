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
		metrics.PlatformCall("reviews", "read", outcome, time.Second)
	}
	families, err := registry.Gather()
	if err != nil || len(families) != 3 {
		t.Fatal(families, err)
	}
	for _, family := range families {
		if family.GetName() == "marketplace_platform_rejections_total" {
			continue
		}
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

func TestPlatformQueueAndRejectionReasonsByClass(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewPlatform(registry)
	reasons := []string{"queue_full", "queue_timeout", "timeout", "tenant_breaker", "server_breaker", "canceled"}
	for _, class := range []string{"checkout", "read"} {
		for _, reason := range reasons {
			metrics.PlatformCall("settings", class, reason, time.Second)
			metrics.PlatformQueueWait(class, reason, time.Second)
		}
		metrics.PlatformQueueWait(class, "admitted", time.Second)
	}
	families, err := registry.Gather()
	if err != nil || len(families) != 4 {
		t.Fatal(families, err)
	}
	for _, family := range families {
		expected := 2 * len(reasons)
		if family.GetName() == "marketplace_platform_queue_wait_seconds" {
			expected += 2
		}
		if len(family.Metric) != expected {
			t.Fatal("потеряна причина или класс", family)
		}
		for _, metric := range family.Metric {
			foundClass := false
			for _, label := range metric.Label {
				if label.GetName() == "class" {
					foundClass = label.GetValue() == "checkout" || label.GetValue() == "read"
				}
				if label.GetName() == "tenantId" || label.GetName() == "tenant" {
					t.Fatal("неограниченная метка", label)
				}
			}
			if !foundClass {
				t.Fatal("нет класса операции", metric)
			}
			if metric.Counter != nil && metric.Counter.GetValue() != 1 {
				t.Fatal(metric)
			}
			if metric.Histogram != nil && metric.Histogram.GetSampleSum() != 1 {
				t.Fatal(metric)
			}
		}
	}
}
