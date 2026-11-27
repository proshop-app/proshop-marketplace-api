// Package jobs — фоновые циклы процесса: разбор outbox, полосы оформления,
// сверки, метрики и чистка служебных записей; Supervisor держит их живыми после паники.
package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// Dispatcher — разбор одной пачки outbox.
type Dispatcher interface {
	RunOnce(ctx context.Context) ([]messaging.Outcome, error)
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
	panics     *prometheus.CounterVec
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

// Run крутит разбор до отмены контекста.
func (r *OutboxRunner) Run(ctx context.Context) error {
	return r.run(ctx, r.dispatcher, nil)
}

// RunLane разбирает свои виды задач независимо и пишет в общие метрики outbox.
func (r *OutboxRunner) RunLane(ctx context.Context, dispatcher Dispatcher) error {
	return r.run(ctx, dispatcher, nil)
}

// RunIndependent ведёт общий цикл и дожидается отдельной полосы при остановке.
func (r *OutboxRunner) RunIndependent(ctx context.Context, lane *messaging.IndependentLane) error {
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		lane.Wait()
	}()
	return r.run(ctx, lane, lane.Done)
}

func (r *OutboxRunner) run(
	ctx context.Context,
	dispatcher Dispatcher,
	wake func() <-chan struct{},
) error {
	for {
		outcomes, err := dispatcher.RunOnce(ctx)
		r.record(outcomes)

		if err != nil && ctx.Err() == nil {
			r.errors.Inc()
			r.logger.Error("outbox: цикл разбора не удался", slog.Any("error", err))
		}

		if err == nil && len(outcomes) > 0 {
			continue
		}

		var done <-chan struct{}
		if err == nil && wake != nil {
			done = wake()
		}
		if !waitForWork(ctx, r.interval, done) {
			return nil
		}
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

func wait(ctx context.Context, d time.Duration) bool {
	return waitForWork(ctx, d, nil)
}

func waitForWork(ctx context.Context, d time.Duration, wake <-chan struct{}) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	case <-wake:
		return true
	}
}
