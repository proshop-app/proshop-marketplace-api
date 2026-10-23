package jobs_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type dispatcher struct {
	mu     sync.Mutex
	rounds [][]messaging.Outcome
	calls  int
}

func (d *dispatcher) RunOnce(context.Context) ([]messaging.Outcome, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.calls++
	if len(d.rounds) == 0 {
		return nil, errors.New("база недоступна")
	}

	round := d.rounds[0]
	d.rounds = d.rounds[1:]

	return round, nil
}

func outcome(kind string, err error) messaging.Outcome {
	return messaging.Outcome{Task: ports.Task{Kind: kind, TenantID: "t1"}, Err: err, Lag: time.Second}
}

func TestOutboxRunnerDrainsFullBatchesAndRecords(t *testing.T) {
	d := &dispatcher{rounds: [][]messaging.Outcome{
		{outcome("a", nil), outcome("a", errors.New("сбой"))},
		{outcome("a", nil)},
	}}
	registry := prometheus.NewRegistry()
	runner := jobs.NewOutboxRunner(d, time.Hour, 2, quiet, registry)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- runner.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for {
		d.mu.Lock()
		calls := d.calls
		d.mu.Unlock()

		if calls >= 2 || time.Now().After(deadline) {
			break
		}

		time.Sleep(time.Millisecond)
	}

	cancel()

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	d.mu.Lock()
	calls := d.calls
	d.mu.Unlock()

	if calls != 2 {
		t.Fatalf("полная пачка — сразу следующая, неполная — пауза: %d вызовов", calls)
	}

	totals := map[string]float64{}
	families, _ := registry.Gather()
	for _, family := range families {
		if family.GetName() != "marketplace_outbox_tasks_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "result" {
					totals[label.GetValue()] += metric.GetCounter().GetValue()
				}
			}
		}
	}

	if totals["done"] != 2 || totals["retry"] != 1 {
		t.Fatalf("метрики задач: %+v", totals)
	}
}

type purger struct {
	mu    sync.Mutex
	calls int
}

func (p *purger) Purge(context.Context) (ports.PurgeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls++

	return ports.PurgeResult{Processed: 1}, nil
}

func TestRetentionRunnerPurgesOnStart(t *testing.T) {
	p := &purger{}
	runner := jobs.NewRetentionRunner(p, time.Hour, quiet)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	if p.calls != 1 {
		t.Fatalf("чистка сразу на старте и не чаще интервала: %d", p.calls)
	}
}

type refresher struct {
	calls int
}

func (r *refresher) Refresh(context.Context) []appdelivery.RefreshResult {
	r.calls++

	return []appdelivery.RefreshResult{
		{Carrier: "cdek", Fetched: 3},
		{Carrier: "yandex", Err: errors.New("сбой")},
		{Carrier: "ozon", Skipped: true},
	}
}

func TestPickupRefreshRunnerRecords(t *testing.T) {
	registry := prometheus.NewRegistry()
	r := &refresher{}
	runner := jobs.NewPickupRefreshRunner(r, time.Hour, quiet, registry)

	runner.RunOnce(context.Background())

	results := map[string]float64{}
	fetched := 0.0
	families, _ := registry.Gather()
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}

			switch family.GetName() {
			case "marketplace_pickup_points_refresh_total":
				results[labels["carrier"]+":"+labels["result"]] += metric.GetCounter().GetValue()
			case "marketplace_pickup_points_fetched":
				fetched += metric.GetGauge().GetValue()
			}
		}
	}

	if results["cdek:done"] != 1 || results["yandex:failed"] != 1 || results["ozon:skipped"] != 1 || fetched != 3 {
		t.Fatalf("метрики обновления: %+v %v", results, fetched)
	}
}

type syncer struct {
	mu    sync.Mutex
	calls int
}

func (s *syncer) Sync(context.Context) (appparticipant.SettingsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls++

	return appparticipant.SettingsResult{Checked: 2, Failed: map[ids.TenantID]error{"t1": errors.New("сбой")}}, nil
}

func TestSettingsSyncRunnerCountsFailures(t *testing.T) {
	registry := prometheus.NewRegistry()
	s := &syncer{}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := jobs.NewSettingsSyncRunner(s, time.Hour, quiet, registry).Run(ctx); err != nil {
		t.Fatal(err)
	}

	families, _ := registry.Gather()
	total := 0.0
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			total += metric.GetCounter().GetValue()
		}
	}

	if s.calls != 1 || total != 1 {
		t.Fatalf("сверка на старте, сбой магазина в метрике: %d %v", s.calls, total)
	}
}
