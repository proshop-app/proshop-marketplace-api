package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
)

// ReferenceStats — состояние словарей реестра для метрик.
type ReferenceStats interface {
	Stats(ctx context.Context) ([]appreference.Stats, error)
}

// ReferenceStatsRunner раз в интервал выставляет ревизию реестра, число
// записей и длину очереди кандидатов: по ним видно, что поставка прошла и
// что очередь оператора растёт.
type ReferenceStatsRunner struct {
	stats      ReferenceStats
	interval   time.Duration
	logger     *slog.Logger
	revision   *prometheus.GaugeVec
	entries    *prometheus.GaugeVec
	candidates *prometheus.GaugeVec
	failures   prometheus.Counter
}

// NewReferenceStatsRunner — метрики реестра с меткой словаря.
func NewReferenceStatsRunner(
	stats ReferenceStats,
	interval time.Duration,
	logger *slog.Logger,
	registerer prometheus.Registerer,
) *ReferenceStatsRunner {
	r := &ReferenceStatsRunner{
		stats:    stats,
		interval: interval,
		logger:   logger,
		revision: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_reference_revision",
				Help: "Голова журнала словаря реестра.",
			},
			[]string{"dictionary"},
		),
		entries: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_reference_entries",
				Help: "Записей в словаре реестра.",
			},
			[]string{"dictionary"},
		),
		candidates: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_reference_candidates",
				Help: "Длина очереди кандидатов словаря реестра.",
			},
			[]string{"dictionary"},
		),
		failures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "marketplace_reference_stats_failures_total",
			Help: "Неудачные чтения состояния реестра для метрик.",
		}),
	}
	registerer.MustRegister(r.revision, r.entries, r.candidates, r.failures)
	return r
}

// RunOnce читает состояние словарей и выставляет метрики.
func (r *ReferenceStatsRunner) RunOnce(ctx context.Context) {
	stats, err := r.stats.Stats(ctx)
	if err != nil {
		r.failures.Inc()
		r.logger.Warn("реестр: метрики не обновлены", slog.Any("error", err))
		return
	}
	for _, s := range stats {
		r.revision.WithLabelValues(s.Dictionary).Set(float64(s.Revision))
		r.entries.WithLabelValues(s.Dictionary).Set(float64(s.Entries))
		r.candidates.WithLabelValues(s.Dictionary).Set(float64(s.Candidates))
	}
}

// Run выставляет метрики сразу и далее раз в интервал до отмены контекста.
func (r *ReferenceStatsRunner) Run(ctx context.Context) error {
	for {
		r.RunOnce(ctx)
		if !wait(ctx, r.interval) {
			return nil
		}
	}
}
