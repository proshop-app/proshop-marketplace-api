// Package jobs — фоновые циклы процесса: разбор outbox, полосы оформления,
// сверки, метрики и чистка служебных записей; Supervisor держит их живыми после паники.
package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// Purger — чистка по сроку.
type Purger interface {
	Purge(ctx context.Context) (ports.PurgeResult, error)
}

// OutboxRunner опустошает готовую очередь и засыпает до следующей работы.
type OutboxRunner struct {
	logger *slog.Logger
	clock  ports.Clock
	tasks  *prometheus.CounterVec
	lag    *prometheus.HistogramVec
	errors prometheus.Counter
	panics *prometheus.CounterVec
}

// RetentionRunner чистит служебные записи по расписанию.
type RetentionRunner struct {
	purger   Purger
	schedule ports.IndexRebuildLock
	interval time.Duration
	logger   *slog.Logger
}

// NewOutboxRunner — цикл разбора outbox с метриками.
func NewOutboxRunner(
	logger *slog.Logger,
	registerer prometheus.Registerer,
	clock ports.Clock,
) *OutboxRunner {
	r := &OutboxRunner{
		logger: logger,
		clock:  clock,
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
		panics: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_outbox_task_panics_total",
				Help: "Паники обработчиков outbox и оформления; задача идёт путём повтора и карантина.",
			},
			[]string{"kind"},
		),
	}
	registerer.MustRegister(r.tasks, r.lag, r.errors, r.panics)

	return r
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
	log := r.logger.With(
		slog.String("kind", outcome.Task.Kind),
		slog.String("tenantId", tenant),
		slog.Int64("taskId", outcome.Task.ID),
		slog.Int("attempt", outcome.Task.Attempts),
	)
	// Стек паники — только в журнал: в базе у задачи остаётся текст ошибки.
	if stack, ok := errs.PanicStack(outcome.Err); ok {
		r.panics.WithLabelValues(outcome.Task.Kind).Inc()
		log.Error("outbox: паника обработчика", slog.Any("error", outcome.Err), slog.String("stack", string(stack)))
	}
	log.Warn(message, slog.String("reason", outcome.FailureReason), slog.Any("error", outcome.Err))
}

// NewRetentionRunner — чистка раз в interval.
func NewRetentionRunner(purger Purger, interval time.Duration, logger *slog.Logger) *RetentionRunner {
	return &RetentionRunner{purger: purger, interval: interval, logger: logger}
}

// WithSchedule назначает общего владельца уборки.
func (r *RetentionRunner) WithSchedule(schedule ports.IndexRebuildLock) *RetentionRunner {
	r.schedule = schedule
	return r
}

// Run чистит сразу и далее раз в интервал до отмены контекста.
func (r *RetentionRunner) Run(ctx context.Context) error {
	for {
		result, err := r.purge(ctx)

		switch {
		case errors.Is(err, ports.ErrRebuildBusy):
		case err != nil && ctx.Err() == nil:
			r.logger.Error("чистка: не удалась", slog.Any("error", err))
		case err == nil:
			r.logger.Info(
				"чистка: готово",
				slog.Int64("bulkApprovals", result.BulkApprovals),
				slog.Int64("processedEvents", result.Processed),
				slog.Int64("purchaseQuarantine", result.PurchaseQuarantine),
				slog.Int64("parkedEvents", result.Parked),
				slog.Int64("revalidationFailures", result.RevalidationFailures),
			)
		}

		if !wait(ctx, r.interval) {
			return nil
		}
	}
}

func (r *RetentionRunner) purge(ctx context.Context) (ports.PurgeResult, error) {
	if r.schedule == nil {
		return r.purger.Purge(ctx)
	}
	var result ports.PurgeResult
	err := r.schedule.WithLock(ctx, func(ctx context.Context) error {
		var err error
		result, err = r.purger.Purge(ctx)
		return err
	})
	return result, err
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
