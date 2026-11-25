package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type reconcileFunc func(context.Context) (appcheckout.ReconcileResult, error)

type reconcileScheduleFunc func(context.Context, func(context.Context) error) error

// releaseProbe — интервал, который сверка может вернуть.
type releaseProbe struct {
	released int
}

func (f reconcileFunc) ReconcileOrders(ctx context.Context) (appcheckout.ReconcileResult, error) {
	return f(ctx)
}

func (f reconcileScheduleFunc) WithLock(ctx context.Context, run func(context.Context) error) error {
	return f(ctx, run)
}

func (reconcileScheduleFunc) Release(context.Context) error { return nil }

func (p *releaseProbe) WithLock(ctx context.Context, run func(context.Context) error) error {
	return run(ctx)
}

func (p *releaseProbe) Release(context.Context) error {
	p.released++
	return nil
}

// Сверка прервана недоступным сервером: найденные расхождения учитываются, интервал возвращается,
// чтобы следующая часовая попытка повторила проход, а не ждала суток.
func TestOrderReconcileInterruptedRecordsAndReleases(t *testing.T) {
	run := reconcileFunc(func(context.Context) (appcheckout.ReconcileResult, error) {
		return appcheckout.ReconcileResult{Checked: 3, Mismatched: 1}, appcheckout.ErrReconcileServerDown
	})
	probe := &releaseProbe{}
	registry := prometheus.NewRegistry()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := NewOrderReconcileRunner(run, probe, logger, registry)
	if err := runner.run(context.Background(), func(context.Context, time.Duration) bool { return false }); err != nil {
		t.Fatal(err)
	}
	var metric dto.Metric
	if err := runner.orders.WithLabelValues("mismatched").Write(&metric); err != nil {
		t.Fatal(err)
	}
	mismatched := metric.GetCounter().GetValue()
	if probe.released != 1 || mismatched != 1 {
		t.Fatalf("итог записан, интервал возвращён: возвратов %d, расхождений %v", probe.released, mismatched)
	}
}

func TestOrderReconcileHourlyAttemptsRespectDailySchedule(t *testing.T) {
	var elapsed time.Duration
	var passes []time.Duration
	attempts := 0
	run := reconcileFunc(func(context.Context) (appcheckout.ReconcileResult, error) {
		passes = append(passes, elapsed)
		return appcheckout.ReconcileResult{
			Checked: 1,
			Failed: map[ids.TenantID]error{
				"t1": errors.New("магазин недоступен"),
			},
		}, nil
	})

	// На старте расписание занято. Как и ScheduledWork, двойник занимает интервал до запуска сверки.
	nextRun := time.Hour
	schedule := reconcileScheduleFunc(func(ctx context.Context, work func(context.Context) error) error {
		expected := time.Duration(attempts) * time.Hour
		if elapsed != expected {
			t.Fatalf("время попытки = %v, ожидается %v", elapsed, expected)
		}
		attempts++
		if elapsed < nextRun {
			return ports.ErrRebuildBusy
		}
		nextRun = elapsed + OrderReconcileInterval
		if err := work(ctx); err != nil {
			t.Fatalf("отказ магазина должен остаться в итоге сверки: %v", err)
		}
		return nil
	})
	waitNextAttempt := func(_ context.Context, interval time.Duration) bool {
		if interval != time.Hour {
			t.Fatalf("интервал между попытками = %v, ожидается час", interval)
		}
		elapsed += interval
		return elapsed <= 25*time.Hour
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := NewOrderReconcileRunner(run, schedule, logger, prometheus.NewRegistry())
	if err := runner.run(context.Background(), waitNextAttempt); err != nil {
		t.Fatal(err)
	}
	expected := []time.Duration{time.Hour, 25 * time.Hour}
	if !slices.Equal(passes, expected) {
		t.Fatalf("время запусков сверки: %v, ожидается %v", passes, expected)
	}
}
