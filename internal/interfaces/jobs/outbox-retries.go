package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// OutboxRetryStats читает число и возраст незавершённых повторов одним срезом.
type OutboxRetryStats interface {
	RetryStats(context.Context) ([]ports.TaskRetryStats, error)
}

// OutboxRetryStatsRunner обновляет метрики независимо от занятых обработчиков outbox.
type OutboxRetryStatsRunner struct {
	kinds    map[string]bool
	source   OutboxRetryStats
	schedule ports.IntervalWork
	clock    ports.Clock
	interval time.Duration
	logger   *slog.Logger
	count    *prometheus.GaugeVec
	age      *prometheus.GaugeVec
	failures prometheus.Counter
}

// NewOutboxRetryStatsRunner не подключает чтение статистики к взятию задач.
func NewOutboxRetryStatsRunner(
	source OutboxRetryStats,
	clock ports.Clock,
	interval time.Duration,
	logger *slog.Logger,
	registerer prometheus.Registerer,
) *OutboxRetryStatsRunner {
	r := &OutboxRetryStatsRunner{
		kinds:    map[string]bool{},
		source:   source,
		clock:    clock,
		interval: interval,
		logger:   logger,
		count: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_outbox_retry_tasks",
				Help: "Незавершённые задачи после ошибки, включая ожидание и повтор в полёте, по виду.",
			},
			[]string{"kind"},
		),
		age: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_outbox_oldest_retry_age_seconds",
				Help: "Возраст от создания самой старой незавершённой задачи в повторе по виду.",
			},
			[]string{"kind"},
		),
		failures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "marketplace_outbox_retry_stats_failures_total",
			Help: "Неудачные чтения среза повторов outbox.",
		}),
	}
	registerer.MustRegister(r.count, r.age, r.failures)
	return r
}

// WithSchedule назначает общего владельца сбора метрик.
func (r *OutboxRetryStatsRunner) WithSchedule(schedule ports.IntervalWork) *OutboxRetryStatsRunner {
	r.schedule = schedule
	return r
}

// Run читает срез сразу и раз в интервал: быстрый разбор очереди не учащает запрос.
func (r *OutboxRetryStatsRunner) Run(ctx context.Context) error {
	for {
		if r.schedule == nil {
			r.RunOnce(ctx)
		} else {
			err := r.schedule.Run(ctx, func(ctx context.Context) error {
				r.RunOnce(ctx)
				return nil
			})
			if errors.Is(err, ports.ErrIntervalTaken) {
				r.count.Reset()
				r.age.Reset()
			} else if err != nil && ctx.Err() == nil {
				r.failures.Inc()
				r.logger.Warn("outbox: расписание метрик повторов недоступно", slog.Any("error", err))
			}
		}
		if !wait(ctx, r.interval) {
			return nil
		}
	}
}

// RunOnce сохраняет последний успешный срез при отказе базы и сбрасывает опустевшие виды.
func (r *OutboxRetryStatsRunner) RunOnce(ctx context.Context) {
	stats, err := r.source.RetryStats(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.failures.Inc()
			r.logger.Warn("outbox: метрики повторов не обновлены", slog.Any("error", err))
		}
		return
	}
	for kind := range r.kinds {
		r.count.WithLabelValues(kind).Set(0)
		r.age.WithLabelValues(kind).Set(0)
	}
	now := r.clock.Now()
	for _, item := range stats {
		r.kinds[item.Kind] = true
		r.count.WithLabelValues(item.Kind).Set(float64(item.Count))
		r.age.WithLabelValues(item.Kind).Set(max(0, now.Sub(item.Oldest).Seconds()))
	}
}
