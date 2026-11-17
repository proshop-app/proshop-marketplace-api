package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// ModelKeyStatsInterval — общий интервал периодического отчёта.
const ModelKeyStatsInterval = 10 * time.Minute

// ModelKeyStatsRunner периодически обновляет метрики покрытия ключами.
type ModelKeyStatsRunner struct {
	schedule   ports.IndexRebuildLock
	stats      ports.ModelKeyStats
	logger     *slog.Logger
	offers     *prometheus.GaugeVec
	shared     *prometheus.GaugeVec
	directions *prometheus.GaugeVec
	schemas    *prometheus.GaugeVec
	incomplete *prometheus.GaugeVec
}

// NewModelKeyStatsRunner создаёт обновление метрик ключей модели.
func NewModelKeyStatsRunner(
	stats ports.ModelKeyStats,
	logger *slog.Logger,
	registerer prometheus.Registerer,
) *ModelKeyStatsRunner {
	runner := &ModelKeyStatsRunner{
		schemas: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_model_key_offers_by_schema",
				Help: "Опубликованные предложения по схеме и источнику ключа.",
			},
			[]string{"schema", "source"},
		),
		incomplete: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_identity_incomplete_share",
				Help: "Доля опубликованных предложений с незаполненной идентичностью по схеме.",
			},
			[]string{"schema"},
		),
		stats:  stats,
		logger: logger,
		offers: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_model_key_offers",
				Help: "Опубликованные предложения по источнику ключа модели.",
			},
			[]string{"source"},
		),
		shared: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_model_key_shared",
				Help: "Ключи модели с двумя и более продавцами.",
			},
			[]string{"source"},
		),
		directions: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_model_key_offers_by_direction",
				Help: "Опубликованные предложения по направлению и источнику ключа.",
			},
			[]string{"direction", "source"},
		),
	}
	registerer.MustRegister(runner.offers, runner.shared, runner.directions, runner.schemas, runner.incomplete)

	return runner
}

// RunOnce обновляет метрики по текущему состоянию каталога.
func (r *ModelKeyStatsRunner) runOnce(ctx context.Context) {
	report, err := r.stats.Report(ctx, false)
	if err != nil {
		r.logger.Warn("ключи моделей: метрики не обновлены", "error", err)
		return
	}
	r.offers.Reset()
	r.shared.Reset()
	r.directions.Reset()
	r.schemas.Reset()
	r.incomplete.Reset()
	for _, source := range []string{"identity", "none"} {
		r.offers.WithLabelValues(source).Set(0)
		r.shared.WithLabelValues(source).Set(0)
	}
	for _, count := range report.Counts {
		switch {
		case count.Schema != "":
			r.schemas.WithLabelValues(count.Schema, count.Source).Set(float64(count.Offers))
			r.incomplete.WithLabelValues(count.Schema).Add(count.IdentityIncompleteShare)
		case count.Direction == "":
			r.offers.WithLabelValues(count.Source).Set(float64(count.Offers))
			r.shared.WithLabelValues(count.Source).Set(float64(count.Shared))
		default:
			r.directions.WithLabelValues(count.Direction, count.Source).Set(float64(count.Offers))
		}
	}
}

// Run обновляет метрики до отмены контекста.
func (r *ModelKeyStatsRunner) Run(ctx context.Context) error {
	for {
		r.RunOnce(ctx)
		if !wait(ctx, ModelKeyStatsInterval) {
			return nil
		}
	}
}

// WithSchedule назначает общее расписание для реплик.
func (r *ModelKeyStatsRunner) WithSchedule(schedule ports.IndexRebuildLock) *ModelKeyStatsRunner {
	r.schedule = schedule
	return r
}

// RunOnce выполняет работу только у владельца текущего интервала.
func (r *ModelKeyStatsRunner) RunOnce(ctx context.Context) {
	if r.schedule == nil {
		r.runOnce(ctx)
		return
	}
	err := r.schedule.WithLock(ctx, func(ctx context.Context) error {
		r.runOnce(ctx)
		return nil
	})
	if err != nil && !errors.Is(err, ports.ErrRebuildBusy) && ctx.Err() == nil {
		r.logger.Warn("расписание фоновой сверки: сбой", slog.Any("error", err))
	}
}
