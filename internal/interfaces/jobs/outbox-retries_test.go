package jobs_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
)

type retryStatsStub struct {
	items   []ports.TaskRetryStats
	err     error
	calls   atomic.Int64
	started chan struct{}
}

func (s *retryStatsStub) RetryStats(context.Context) ([]ports.TaskRetryStats, error) {
	if s.calls.Add(1) == 1 && s.started != nil {
		close(s.started)
	}
	return s.items, s.err
}

func TestRetryMetricsAgeCountAndEmptySnapshot(t *testing.T) {
	now := time.Now()
	clock := fake.NewClock(now)
	source := &retryStatsStub{items: []ports.TaskRetryStats{
		{Kind: "index", Count: 7, Oldest: now.Add(-30 * time.Minute)},
	}}
	registry := prometheus.NewRegistry()
	runner := jobs.NewOutboxRetryStatsRunner(source, clock, time.Hour, quiet, registry)
	runner.RunOnce(context.Background())
	metric := func(name string) float64 {
		t.Helper()
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range families {
			if family.GetName() == name {
				return family.Metric[0].GetGauge().GetValue()
			}
		}
		t.Fatal("нет метрики", name)
		return 0
	}
	if metric("marketplace_outbox_retry_tasks") != 7 || metric("marketplace_outbox_oldest_retry_age_seconds") != 1800 {
		t.Fatal("не видны ожидающие повторы")
	}
	source.err = errors.New("база недоступна")
	runner.RunOnce(context.Background())
	if metric("marketplace_outbox_retry_tasks") != 7 {
		t.Fatal("отказ затёр последний срез")
	}
	source.err, source.items = nil, nil
	runner.RunOnce(context.Background())
	if metric("marketplace_outbox_retry_tasks") != 0 || metric("marketplace_outbox_oldest_retry_age_seconds") != 0 {
		t.Fatal("опустевший вид остался в метриках")
	}
	if count, err := registry.Gather(); err != nil || len(count) != 3 {
		t.Fatal(count, err)
	}
}

func TestRetrySnapshotDoesNotFollowQueueDrainRate(t *testing.T) {
	source := &retryStatsStub{started: make(chan struct{})}
	clock := fake.NewClock(time.Now())
	runner := jobs.NewOutboxRetryStatsRunner(source, clock, time.Hour, quiet, prometheus.NewRegistry())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	<-source.started
	store := fake.NewStore()
	if err := store.Do(
		ctx,
		func(ctx context.Context, tx ports.Tx) error {
			for range 20 {
				task := ports.NewTask{Kind: "work", TenantID: "t1", At: clock.Now()}
				if err := tx.Outbox().Enqueue(ctx, task); err != nil {
					return err
				}
			}
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	dispatcher := messaging.NewDispatcher(
		store,
		clock,
		messaging.DispatcherConfig{
			Batch:     1,
			PerTenant: 1,
			Lease:     time.Minute,
		},
		map[string]messaging.Handler{"work": func(context.Context, ports.Task) error { return nil }},
	)
	for range 20 {
		if _, _, _, err := dispatcher.RunWork(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if source.calls.Load() != 1 {
		t.Fatal("каждый Claim читает метрики", source.calls.Load())
	}
}
