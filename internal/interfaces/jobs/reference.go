package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
)

// ReferenceRefresher — сверка копии общих словарей с владельцем platform.
type ReferenceRefresher interface {
	Refresh(ctx context.Context) (apptaxonomy.ReferenceRefresh, error)
}

// ReferenceRefreshRunner сверяет копию закрытых словарей раз в интервал.
type ReferenceRefreshRunner struct {
	refresher ReferenceRefresher
	interval  time.Duration
	logger    *slog.Logger
	runs      *prometheus.CounterVec
	age       prometheus.Gauge
}

// NewReferenceRefreshRunner — сверка с метриками итога и давности копии.
func NewReferenceRefreshRunner(
	refresher ReferenceRefresher,
	interval time.Duration,
	logger *slog.Logger,
	registerer prometheus.Registerer,
) *ReferenceRefreshRunner {
	r := &ReferenceRefreshRunner{
		refresher: refresher,
		interval:  interval,
		logger:    logger,
		runs: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_reference_refresh_total",
				Help: "Сверки копии общих словарей: done, partial (часть словарей не перечитана), failed.",
			},
			[]string{"result"},
		),
		age: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "marketplace_reference_values_age_seconds",
			Help: "Сколько не подтверждался владельцем самый давний словарь копии.",
		}),
	}
	registerer.MustRegister(r.runs, r.age)

	return r
}

// RunOnce сверяет копию и записывает итог. Об изменениях пишет журнал,
// о спокойной сверке — только метрика.
func (r *ReferenceRefreshRunner) RunOnce(ctx context.Context) {
	result, err := r.refresher.Refresh(ctx)
	r.age.Set(result.Age.Seconds())

	switch {
	case err != nil && ctx.Err() == nil:
		r.runs.WithLabelValues("failed").Inc()
		r.logger.Warn(
			"общие словари: владелец не ответил, копия прежняя",
			slog.Duration("age", result.Age),
			slog.Any("error", err),
		)
	case err != nil:
		return
	case len(result.Failed) > 0:
		r.runs.WithLabelValues("partial").Inc()
		for code, cause := range result.Failed {
			r.logger.Warn(
				"общий словарь: не перечитан",
				slog.String("dictionary", code),
				slog.Any("error", cause),
			)
		}
	default:
		r.runs.WithLabelValues("done").Inc()
	}

	if len(result.Updated) > 0 || len(result.Dropped) > 0 {
		r.logger.Info(
			"общие словари: копия обновлена",
			slog.Any("updated", result.Updated),
			slog.Any("dropped", result.Dropped),
		)
	}
}

// Run сверяет сразу и далее раз в интервал до отмены контекста.
func (r *ReferenceRefreshRunner) Run(ctx context.Context) error {
	for {
		r.RunOnce(ctx)

		if !wait(ctx, r.interval) {
			return nil
		}
	}
}
