package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const modelKeyStatsInterval = 10 * time.Minute

// ModelKeyStatsRunner периодически обновляет метрики покрытия ключами.
type ModelKeyStatsRunner struct {
	stats      ports.ModelKeyStats
	logger     *slog.Logger
	offers     *prometheus.GaugeVec
	shared     *prometheus.GaugeVec
	directions *prometheus.GaugeVec
}

// NewModelKeyStatsRunner создаёт обновление метрик ключей модели.
func NewModelKeyStatsRunner(
	stats ports.ModelKeyStats,
	logger *slog.Logger,
	registerer prometheus.Registerer,
) *ModelKeyStatsRunner {
	runner := &ModelKeyStatsRunner{
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
	registerer.MustRegister(runner.offers, runner.shared, runner.directions)

	return runner
}

// RunOnce обновляет метрики по текущему состоянию каталога.
func (r *ModelKeyStatsRunner) RunOnce(ctx context.Context) {
	report, err := r.stats.Report(ctx, false)
	if err != nil {
		r.logger.Warn("ключи моделей: метрики не обновлены", "error", err)
		return
	}
	r.offers.Reset()
	r.shared.Reset()
	r.directions.Reset()
	for _, source := range []string{"gtin", "mpn", "none"} {
		r.offers.WithLabelValues(source).Set(0)
		r.shared.WithLabelValues(source).Set(0)
	}
	for _, count := range report.Counts {
		if count.Direction == "" {
			r.offers.WithLabelValues(count.Source).Set(float64(count.Offers))
			r.shared.WithLabelValues(count.Source).Set(float64(count.Shared))
		} else {
			r.directions.WithLabelValues(count.Direction, count.Source).Set(float64(count.Offers))
		}
	}
}

// Run обновляет метрики до отмены контекста.
func (r *ModelKeyStatsRunner) Run(ctx context.Context) error {
	for {
		r.RunOnce(ctx)
		if !wait(ctx, modelKeyStatsInterval) {
			return nil
		}
	}
}
