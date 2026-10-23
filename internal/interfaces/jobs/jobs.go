// Package jobs — фоновые циклы процесса: разбор outbox и чистка служебных
// записей по сроку.
package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// Dispatcher — разбор одной пачки outbox.
type Dispatcher interface {
	RunOnce(ctx context.Context) ([]messaging.Outcome, error)
}

// Purger — чистка по сроку.
type Purger interface {
	Purge(ctx context.Context) (ports.PurgeResult, error)
}

// OutboxRunner крутит разбор outbox: полная пачка — сразу следующая,
// неполная — пауза до следующего опроса.
type OutboxRunner struct {
	dispatcher Dispatcher
	interval   time.Duration
	batch      int
	logger     *slog.Logger
	tasks      *prometheus.CounterVec
	lag        *prometheus.HistogramVec
	errors     prometheus.Counter
}

// RetentionRunner чистит служебные записи по расписанию.
type RetentionRunner struct {
	purger   Purger
	interval time.Duration
	logger   *slog.Logger
}

// NewOutboxRunner — цикл разбора outbox с метриками.
func NewOutboxRunner(
	dispatcher Dispatcher,
	interval time.Duration,
	batch int,
	logger *slog.Logger,
	registerer prometheus.Registerer,
) *OutboxRunner {
	r := &OutboxRunner{
		dispatcher: dispatcher,
		interval:   interval,
		batch:      batch,
		logger:     logger,
		tasks: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_outbox_tasks_total",
				Help: "Задачи outbox по итогу: done — выполнена, retry — отложена на повтор.",
			},
			[]string{"kind", "tenant", "result"},
		),
		lag: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "marketplace_outbox_task_lag_seconds",
				Help:    "Время от постановки задачи до её выполнения.",
				Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 300, 900},
			},
			[]string{"kind"},
		),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "marketplace_outbox_cycle_errors_total",
			Help: "Сбои цикла разбора outbox: взятие или отметка задач.",
		}),
	}
	registerer.MustRegister(r.tasks, r.lag, r.errors)

	return r
}

// Run крутит разбор до отмены контекста.
func (r *OutboxRunner) Run(ctx context.Context) error {
	for {
		outcomes, err := r.dispatcher.RunOnce(ctx)
		r.record(outcomes)

		if err != nil && ctx.Err() == nil {
			r.errors.Inc()
			r.logger.Error("outbox: цикл разбора не удался", slog.Any("error", err))
		}

		if err == nil && len(outcomes) == r.batch {
			continue
		}

		if !wait(ctx, r.interval) {
			return nil
		}
	}
}

func (r *OutboxRunner) record(outcomes []messaging.Outcome) {
	for _, outcome := range outcomes {
		tenant := string(outcome.Task.TenantID)

		if outcome.Err == nil {
			r.tasks.WithLabelValues(outcome.Task.Kind, tenant, "done").Inc()
			r.lag.WithLabelValues(outcome.Task.Kind).Observe(outcome.Lag.Seconds())

			continue
		}

		r.tasks.WithLabelValues(outcome.Task.Kind, tenant, "retry").Inc()
		r.logger.Warn(
			"outbox: задача отложена",
			slog.String("kind", outcome.Task.Kind),
			slog.String("tenantId", tenant),
			slog.Int64(
				"taskId",
				outcome.Task.ID,
			),
			slog.Int("attempt", outcome.Task.Attempts),
			slog.Any("error", outcome.Err),
		)
	}
}

// NewRetentionRunner — чистка раз в interval.
func NewRetentionRunner(purger Purger, interval time.Duration, logger *slog.Logger) *RetentionRunner {
	return &RetentionRunner{purger: purger, interval: interval, logger: logger}
}

// Run чистит сразу и далее раз в интервал до отмены контекста.
func (r *RetentionRunner) Run(ctx context.Context) error {
	for {
		result, err := r.purger.Purge(ctx)

		switch {
		case err != nil && ctx.Err() == nil:
			r.logger.Error("чистка: не удалась", slog.Any("error", err))
		case err == nil:
			r.logger.Info(
				"чистка: готово",
				slog.Int64("processedEvents", result.Processed),
				slog.Int64("outboxDone", result.OutboxDone),
				slog.Int64("parkedEvents", result.Parked),
			)
		}

		if !wait(ctx, r.interval) {
			return nil
		}
	}
}

func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
