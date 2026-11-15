package jobs_test

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
)

type identityStats struct {
	report ports.ModelKeyReport
}

func (s *identityStats) Report(context.Context, bool) (ports.ModelKeyReport, error) {
	return s.report, nil
}

func TestIdentityMetricsBySchema(t *testing.T) {
	stats := &identityStats{
		report: ports.ModelKeyReport{
			Counts: []ports.ModelKeyCount{
				{Source: "identity", Offers: 3, Shared: 1},
				{Schema: "s1", Source: "identity", Offers: 3, IdentityIncompleteShare: 0},
				{Schema: "s1", Source: "none", Offers: 1, IdentityIncompleteShare: 0.25},
			},
		},
	}
	registry := prometheus.NewRegistry()
	runner := jobs.NewModelKeyStatsRunner(stats, quiet, registry)
	for i := 0; i < 2; i++ {
		runner.RunOnce(context.Background())
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, family := range families {
			if family.GetName() != "marketplace_identity_incomplete_share" {
				continue
			}
			found = true
			metrics := family.GetMetric()
			if len(metrics) != 1 || metrics[0].GetGauge().GetValue() != 0.25 ||
				metrics[0].GetLabel()[0].GetValue() != "s1" {
				t.Fatal("доля должна относиться к схеме и не накапливаться между обходами", metrics)
			}
		}
		if !found {
			t.Fatal("нет метрики неполной идентичности")
		}
	}
}
