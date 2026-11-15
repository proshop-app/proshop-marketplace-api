package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
)

// SettingsSyncer — сверка настроек магазинов с сервером.
type SettingsSyncer interface {
	Sync(ctx context.Context) (appparticipant.SettingsResult, error)
}

// SettingsSyncRunner сверяет настройки магазинов раз в интервал.
type SettingsSyncRunner struct {
	candidates prometheus.Gauge
	queue      prometheus.Gauge
	syncer     SettingsSyncer
	interval   time.Duration
	logger     *slog.Logger
	failures   *prometheus.CounterVec
}

// NewSettingsSyncRunner — сверка настроек с метрикой сбоев по магазину.
func NewSettingsSyncRunner(
	syncer SettingsSyncer,
	interval time.Duration,
	logger *slog.Logger,
	registerer prometheus.Registerer,
) *SettingsSyncRunner {
	r := &SettingsSyncRunner{
		candidates: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "marketplace_participant_candidates",
			Help: "Число кандидатов площадки.",
		}),
		queue: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "marketplace_participant_queue",
			Help: "Кандидаты с валидной доставкой, ожидающие оператора.",
		}),
		syncer:   syncer,
		interval: interval,
		logger:   logger,
		failures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_shop_settings_sync_failures_total",
				Help: "Неудачные сверки настроек магазина с сервером.",
			},
			[]string{"tenant"},
		),
	}
	registerer.MustRegister(r.failures, r.candidates, r.queue)

	return r
}

// Run сверяет сразу и далее раз в интервал до отмены контекста.
func (r *SettingsSyncRunner) Run(ctx context.Context) error {
	for {
		result, err := r.syncer.Sync(ctx)

		switch {
		case err != nil && ctx.Err() == nil:
			r.logger.Error("настройки магазинов: сверка не удалась", slog.Any("error", err))
		case err == nil:
			r.candidates.Set(float64(result.Candidates))
			r.queue.Set(float64(result.Queue))
			for tenant, cause := range result.Failed {
				r.failures.WithLabelValues(string(tenant)).Inc()
				r.logger.Warn(
					"настройки магазина: сервер не ответил",
					slog.String("tenantId", string(tenant)),
					slog.Any("error", cause),
				)
			}

			r.logger.Info(
				"настройки магазинов: сверены",
				slog.Int("checked", result.Checked),
				slog.Int("changed", result.Changed),
				slog.Int("failed", len(result.Failed)),
			)
		}

		if !wait(ctx, r.interval) {
			return nil
		}
	}
}
