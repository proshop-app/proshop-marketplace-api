package jobs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
)

type referenceStats struct {
	stats []appreference.Stats
	err   error
}

func (s referenceStats) Stats(context.Context) ([]appreference.Stats, error) {
	return s.stats, s.err
}

func TestReferenceStatsRunnerSetsGauges(t *testing.T) {
	registry := prometheus.NewRegistry()
	stats := referenceStats{stats: []appreference.Stats{{Dictionary: "b1", Revision: 7, Entries: 3, Candidates: 2}}}
	jobs.NewReferenceStatsRunner(stats, 0, quiet, registry).RunOnce(context.Background())
	values := gauges(t, registry)
	if values["marketplace_reference_revision"] != 7 || values["marketplace_reference_entries"] != 3 ||
		values["marketplace_reference_candidates"] != 2 {
		t.Fatalf("метрики реестра: %v", values)
	}

	failing := prometheus.NewRegistry()
	jobs.NewReferenceStatsRunner(referenceStats{err: errors.New("база недоступна")}, 0, quiet, failing).
		RunOnce(context.Background())
	if gauges(t, failing)["marketplace_reference_stats_failures_total"] != 1 {
		t.Fatal("сбой чтения не посчитан")
	}
}

func gauges(t *testing.T, registry *prometheus.Registry) map[string]float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]float64{}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			switch {
			case metric.GetGauge() != nil:
				values[family.GetName()] = metric.GetGauge().GetValue()
			case metric.GetCounter() != nil:
				values[family.GetName()] = metric.GetCounter().GetValue()
			}
		}
	}
	return values
}
