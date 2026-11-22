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

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type reconcileFunc func(context.Context) (appcheckout.ReconcileResult, error)

type reconcileScheduleFunc func(context.Context, func(context.Context) error) error

func (f reconcileFunc) ReconcileOrders(ctx context.Context) (appcheckout.ReconcileResult, error) {
	return f(ctx)
}

func (f reconcileScheduleFunc) WithLock(ctx context.Context, run func(context.Context) error) error {
	return f(ctx, run)
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
