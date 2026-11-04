package jobs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
)

type referenceRefresher struct {
	results []apptaxonomy.ReferenceRefresh
	errs    []error
}

func (r *referenceRefresher) Refresh(context.Context) (apptaxonomy.ReferenceRefresh, error) {
	result, err := r.results[0], r.errs[0]
	r.results, r.errs = r.results[1:], r.errs[1:]

	return result, err
}

func TestReferenceRefreshRunnerRecordsResultAndAge(t *testing.T) {
	registry := prometheus.NewRegistry()
	refresher := &referenceRefresher{
		results: []apptaxonomy.ReferenceRefresh{
			{Checked: 1, Updated: []string{"a1"}, Age: 5 * time.Second},
			{Checked: 2, Failed: map[string]error{"a2": errors.New("сбой")}},
			{Age: 90 * time.Second},
		},
		errs: []error{nil, nil, errors.New("владелец недоступен")},
	}
	runner := jobs.NewReferenceRefreshRunner(refresher, time.Hour, quiet, registry)
	for range 3 {
		runner.RunOnce(context.Background())
	}

	results := map[string]float64{}
	age := 0.0
	families, _ := registry.Gather()
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			switch family.GetName() {
			case "marketplace_reference_refresh_total":
				results[metric.GetLabel()[0].GetValue()] += metric.GetCounter().GetValue()
			case "marketplace_reference_values_age_seconds":
				age = metric.GetGauge().GetValue()
			}
		}
	}

	if results["done"] != 1 || results["partial"] != 1 || results["failed"] != 1 || age != 90 {
		t.Fatalf("метрики сверки: %+v %v", results, age)
	}
}
