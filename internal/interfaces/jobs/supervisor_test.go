package jobs_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

func TestSupervisorContinuesLoopAfterPanic(t *testing.T) {
	var journal bytes.Buffer
	registry := prometheus.NewRegistry()
	supervisor := jobs.NewSupervisor(slog.New(slog.NewTextHandler(&journal, nil)), registry)
	stopped := errors.New("цикл остановлен")
	runs := 0
	err := supervisor.Run(context.Background(), jobs.Loop{Name: "retention", Run: func(context.Context) error {
		runs++
		if runs == 1 {
			panic("сбой чистки")
		}
		return stopped
	}})

	if !errors.Is(err, stopped) || runs != 2 {
		t.Fatalf("после паники цикл запущен снова: запусков %d, %v", runs, err)
	}
	if got := panicCount(t, registry, "marketplace_job_panics_total"); got != 1 {
		t.Fatalf("метрика паник цикла: %v", got)
	}
	if !strings.Contains(journal.String(), "job=retention") || !strings.Contains(journal.String(), "goroutine") {
		t.Fatalf("в журнале цикл и стек паники: %s", journal.String())
	}
}

func TestSupervisorStopsDuringPauseOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := jobs.NewSupervisor(quiet, prometheus.NewRegistry())
	err := supervisor.Run(ctx, jobs.Loop{Name: "outbox", Run: func(context.Context) error {
		cancel()
		panic("сбой разбора")
	}})
	if err != nil {
		t.Fatalf("остановка процесса во время паузы — не ошибка цикла: %v", err)
	}
}

func TestOutboxOutcomeCountsPanic(t *testing.T) {
	registry := prometheus.NewRegistry()
	runner := jobs.NewOutboxRunner(quiet, registry, fake.NewClock(time.Unix(0, 0)))
	panicked := errs.Guard(func() error { panic("сбой обработчика") })
	runner.RecordOutcome(messaging.Outcome{Err: panicked, FailureReason: "attempts_exhausted"})
	runner.RecordOutcome(messaging.Outcome{Err: errors.New("сеть")})

	if got := panicCount(t, registry, "marketplace_outbox_task_panics_total"); got != 1 {
		t.Fatalf("паники считаются отдельно от обычных сбоев: %v", got)
	}
}

func panicCount(t *testing.T, registry *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	total := 0.0
	for _, family := range families {
		if family.GetName() == name {
			for _, metric := range family.Metric {
				total += metric.GetCounter().GetValue()
			}
		}
	}
	return total
}
