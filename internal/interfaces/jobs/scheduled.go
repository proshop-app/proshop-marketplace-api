package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// SettingsSyncer — сверка настроек магазинов с сервером.
type SettingsSyncer interface {
	Sync(ctx context.Context) (appparticipant.SettingsResult, error)
}

// SettingsSyncRunner сверяет настройки магазинов раз в интервал.
type SettingsSyncRunner struct {
	schedule   ports.IndexRebuildLock
	candidates prometheus.Gauge
	queue      prometheus.Gauge
	syncer     SettingsSyncer
	interval   time.Duration
	logger     *slog.Logger
	failures   prometheus.Counter
}

// NewSettingsSyncRunner — сверка настроек с метрикой сбоев.
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
		failures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "marketplace_shop_settings_sync_failures_total",
			Help: "Неудачные сверки настроек магазинов с сервером; магазин — в журнале.",
		}),
	}
	registerer.MustRegister(r.failures, r.candidates, r.queue)

	return r
}

// Run сверяет сразу и далее раз в интервал до отмены контекста.
func (r *SettingsSyncRunner) Run(ctx context.Context) error {
	for {
		result, err := r.sync(ctx)

		switch {
		case errors.Is(err, ports.ErrRebuildBusy):
		case err != nil && ctx.Err() == nil:
			r.logger.Error("настройки магазинов: сверка не удалась", slog.Any("error", err))
		case err == nil:
			r.candidates.Set(float64(result.Candidates))
			r.queue.Set(float64(result.Queue))
			for tenant, cause := range result.Failed {
				r.failures.Inc()
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

// WithSchedule назначает общего владельца интервала для всех реплик.
func (r *SettingsSyncRunner) WithSchedule(schedule ports.IndexRebuildLock) *SettingsSyncRunner {
	r.schedule = schedule
	return r
}

func (r *SettingsSyncRunner) sync(ctx context.Context) (appparticipant.SettingsResult, error) {
	if r.schedule == nil {
		return r.syncer.Sync(ctx)
	}
	var result appparticipant.SettingsResult
	err := r.schedule.WithLock(ctx, func(ctx context.Context) error {
		var err error
		result, err = r.syncer.Sync(ctx)
		return err
	})
	return result, err
}
