// Package jobs — фоновые циклы процесса: разбор outbox и чистка служебных
// записей по сроку.
package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// Dispatcher — разбор одной пачки outbox.
type Dispatcher interface {
	RunOnce(ctx context.Context) ([]messaging.Outcome, error)
	ReadyCounts(context.Context) (map[string]int64, error)
}

// Purger — чистка по сроку.
type Purger interface {
	Purge(ctx context.Context) (ports.PurgeResult, error)
}

// OutboxRunner опустошает готовую очередь, затем ждёт следующего опроса.
type OutboxRunner struct {
	dispatcher Dispatcher
	interval   time.Duration
	logger     *slog.Logger
	tasks      *prometheus.CounterVec
	lag        *prometheus.HistogramVec
	errors     prometheus.Counter
	ready      *prometheus.GaugeVec
	depthAt    time.Time
	readyKinds map[string]bool
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
	logger *slog.Logger,
	registerer prometheus.Registerer,
) *OutboxRunner {
	r := &OutboxRunner{
		dispatcher: dispatcher,
		interval:   interval,
		logger:     logger,
		readyKinds: map[string]bool{},
		tasks: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_outbox_tasks_total",
				Help: "Задачи outbox по итогу: done — выполнена, retry — повтор, quarantined — отказ.",
			},
			[]string{"kind", "tenant", "result"},
		),
		lag: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "marketplace_outbox_task_lag_seconds",
				Help:    "Время от постановки задачи до её выполнения.",
				Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 300, 900, 1800, 3600, 10800, 21600},
			},
			[]string{"kind"},
		),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "marketplace_outbox_cycle_errors_total",
			Help: "Сбои цикла разбора outbox: взятие или отметка задач.",
		}),
		ready: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_outbox_ready_tasks",
				Help: "Готовые задачи outbox по виду, без действующих аренд и отложенных повторов.",
			},
			[]string{"kind"},
		),
	}
	registerer.MustRegister(r.tasks, r.lag, r.errors, r.ready)

	return r
}

// Run крутит разбор до отмены контекста.
func (r *OutboxRunner) Run(ctx context.Context) error {
	return r.run(ctx, r.dispatcher, true)
}

// RunLane разбирает свои виды задач независимо и пишет в общие метрики outbox.
func (r *OutboxRunner) RunLane(ctx context.Context, dispatcher Dispatcher) error {
	return r.run(ctx, dispatcher, false)
}

func (r *OutboxRunner) run(ctx context.Context, dispatcher Dispatcher, countDepth bool) error {
	for {
		if countDepth && time.Now().After(r.depthAt) {
			r.recordDepth(ctx)
			r.depthAt = time.Now().Add(r.interval)
		}
		outcomes, err := dispatcher.RunOnce(ctx)
		r.record(outcomes)

		if err != nil && ctx.Err() == nil {
			r.errors.Inc()
			r.logger.Error("outbox: цикл разбора не удался", slog.Any("error", err))
		}

		if err == nil && len(outcomes) > 0 {
			continue
		}

		if !wait(ctx, r.interval) {
			return nil
		}
	}
}

func (r *OutboxRunner) recordDepth(ctx context.Context) {
	counts, err := r.dispatcher.ReadyCounts(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.errors.Inc()
			r.logger.Error("outbox: подсчёт готовых задач не удался", slog.Any("error", err))
		}
		return
	}
	for kind := range r.readyKinds {
		r.ready.WithLabelValues(kind).Set(0)
	}
	for kind, count := range counts {
		r.readyKinds[kind] = true
		r.ready.WithLabelValues(kind).Set(float64(count))
	}
}

func (r *OutboxRunner) record(outcomes []messaging.Outcome) {
	for _, outcome := range outcomes {
		r.RecordOutcome(outcome)
	}
}

// RecordOutcome учитывает результаты всех полос общего outbox.
func (r *OutboxRunner) RecordOutcome(outcome messaging.Outcome) {
	tenant := string(outcome.Task.TenantID)

	if outcome.Err == nil {
		r.tasks.WithLabelValues(outcome.Task.Kind, tenant, "done").Inc()
		r.lag.WithLabelValues(outcome.Task.Kind).Observe(outcome.Lag.Seconds())

		return
	}

	result, message := "retry", "outbox: задача отложена"
	if outcome.FailureReason != "" {
		result, message = "quarantined", "outbox: задача в карантине"
	}
	r.tasks.WithLabelValues(outcome.Task.Kind, tenant, result).Inc()
	if appcheckout.CreationPending(outcome.Err) {
		return
	}
	r.logger.Warn(
		message,
		slog.String("reason", outcome.FailureReason),
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
				slog.Int64("bulkApprovals", result.BulkApprovals),
				slog.Int64("processedEvents", result.Processed),
				slog.Int64("outboxDone", result.OutboxDone),
				slog.Int64("parkedEvents", result.Parked),
				slog.Int64("revalidationFailures", result.RevalidationFailures),
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
