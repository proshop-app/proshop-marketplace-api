package jobs_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type dispatcher struct {
	mu     sync.Mutex
	rounds [][]messaging.Outcome
	calls  int
}

type purger struct {
	mu    sync.Mutex
	calls int
}

type syncer struct {
	mu    sync.Mutex
	calls int
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

func (d *dispatcher) ReadyLanes(context.Context) ([]ports.ReadyLane, error) {
	return []ports.ReadyLane{{Kind: "a", Tenants: 3, Oldest: time.Now().Add(-time.Minute)}}, nil
}

func outcome(kind string, err error) messaging.Outcome {
	return messaging.Outcome{Task: ports.Task{Kind: kind, TenantID: "t1"}, Err: err, Lag: time.Second}
}

func TestOutboxRunnerDrainsFullBatchesAndRecords(t *testing.T) {
	d := &dispatcher{rounds: [][]messaging.Outcome{
		{outcome("a", nil), outcome("a", errors.New("сбой"))},
		{outcome("a", nil)},
		{},
	}}
	registry := prometheus.NewRegistry()
	runner := jobs.NewOutboxRunner(d, time.Hour, quiet, registry)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- runner.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for {
		d.mu.Lock()
		calls := d.calls
		d.mu.Unlock()

		if calls >= 3 || time.Now().After(deadline) {
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

	if calls != 3 {
		t.Fatalf("следующая пачка сразу, пустая очередь — пауза: %d вызовов", calls)
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

func TestOutboxRunnerEmptyQueueWaits(t *testing.T) {
	d := &dispatcher{rounds: [][]messaging.Outcome{{}}}
	runner := jobs.NewOutboxRunner(d, time.Hour, quiet, prometheus.NewRegistry())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if d.calls != 1 {
		t.Fatalf("пустая очередь должна ждать опроса, вызовов: %d", d.calls)
	}
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

func (s *syncer) Sync(context.Context) (appparticipant.SettingsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls++

	return appparticipant.SettingsResult{
		Checked:    2,
		Candidates: 3,
		Queue:      1,
		Failed:     map[ids.TenantID]error{"t1": errors.New("сбой")},
	}, nil
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
	counts := map[string]float64{}
	for _, family := range families {
		if strings.HasPrefix(family.GetName(), "marketplace_participant_") {
			counts[family.GetName()] = family.GetMetric()[0].GetGauge().GetValue()
		}
	}
	if counts["marketplace_participant_candidates"] != 3 || counts["marketplace_participant_queue"] != 1 {
		t.Fatal(counts)
	}
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

func TestCheckoutOutboxMetricsAndRoutineWaiting(t *testing.T) {
	registry := prometheus.NewRegistry()
	var journal strings.Builder
	logger := slog.New(slog.NewTextHandler(&journal, nil))
	runner := jobs.NewOutboxRunner(&dispatcher{}, time.Second, logger, registry)
	for _, kind := range []string{
		appcheckout.TaskCreateOrder,
		appcheckout.TaskExpireCreation,
		appcheckout.TaskPollOrder,
	} {
		runner.RecordOutcome(outcome(kind, nil))
	}
	pending := errs.New(errs.Unavailable, "checkout.creation_pending", "ожидание")
	runner.RecordOutcome(outcome(appcheckout.TaskExpireCreation, pending))
	if journal.Len() != 0 {
		t.Fatal("штатное ожидание не должно засорять журнал", journal.String())
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	done, retry, observed := 0.0, 0.0, uint64(0)
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			switch family.GetName() {
			case "marketplace_outbox_tasks_total":
				for _, label := range metric.GetLabel() {
					if label.GetName() == "result" && label.GetValue() == "done" {
						done += metric.GetCounter().GetValue()
					}
					if label.GetName() == "result" && label.GetValue() == "retry" {
						retry += metric.GetCounter().GetValue()
					}
				}
			case "marketplace_outbox_task_lag_seconds":
				observed += metric.GetHistogram().GetSampleCount()
			}
		}
	}
	if done != 3 || retry != 1 || observed != 3 {
		t.Fatal(done, retry, observed)
	}
	runner.RecordOutcome(outcome(appcheckout.TaskCreateOrder, errors.New("хранилище недоступно")))
	if !strings.Contains(journal.String(), "tenantId=t1") {
		t.Fatal(journal.String())
	}
}

// Готовую работу снимает только владелец интервала; реплика без владения сбрасывает свои значения.
func TestOutboxReadyRunnerOnlyOwnerReports(t *testing.T) {
	registry := prometheus.NewRegistry()
	schedule := &fake.IndexRebuildLock{}
	runner := jobs.NewOutboxReadyRunner(&dispatcher{}, schedule, quiet, registry)
	runner.RunOnce(context.Background())
	if got := readyGauges(t, registry); got["marketplace_outbox_ready_tenants"] != 3 ||
		got["marketplace_outbox_ready_age_seconds"] < 60 {
		t.Fatalf("владелец: %+v", got)
	}
	// Интервал занят другой репликой: свои значения не отдаём.
	err := schedule.WithLock(context.Background(), func(ctx context.Context) error {
		runner.RunOnce(ctx)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := readyGauges(t, registry); len(got) != 0 {
		t.Fatalf("не владелец держит значения: %+v", got)
	}
}

func readyGauges(t *testing.T, registry *prometheus.Registry) map[string]float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	ready := map[string]float64{}
	for _, family := range families {
		gauge := family.GetType() == dto.MetricType_GAUGE
		if gauge && strings.HasPrefix(family.GetName(), "marketplace_outbox_ready_") {
			ready[family.GetName()] = family.GetMetric()[0].GetGauge().GetValue()
		}
	}
	return ready
}
