package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
)

// Refresher — обновление справочников пунктов выдачи.
type Refresher interface {
	Refresh(ctx context.Context) []appdelivery.RefreshResult
}

// SettingsSyncer — сверка настроек магазинов с сервером.
type SettingsSyncer interface {
	Sync(ctx context.Context) (appparticipant.SettingsResult, error)
}

// PickupRefreshRunner обновляет справочники пунктов выдачи раз в интервал.
type PickupRefreshRunner struct {
	refresher Refresher
	interval  time.Duration
	logger    *slog.Logger
	runs      *prometheus.CounterVec
	fetched   *prometheus.GaugeVec
}

// NewPickupRefreshRunner — обновление справочников с метриками по перевозчику.
func NewPickupRefreshRunner(refresher Refresher, interval time.Duration, logger *slog.Logger, registerer prometheus.Registerer) *PickupRefreshRunner {
	r := &PickupRefreshRunner{
		refresher: refresher, interval: interval, logger: logger,
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "marketplace_pickup_points_refresh_total",
			Help: "Обновления справочников пунктов выдачи: done, failed, skipped (обновляет другой процесс).",
		}, []string{"carrier", "result"}),
		fetched: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "marketplace_pickup_points_fetched",
			Help: "Пунктов выдачи в последней полной выгрузке перевозчика.",
		}, []string{"carrier"}),
	}
	registerer.MustRegister(r.runs, r.fetched)

	return r
}

// RunOnce обновляет справочники и записывает итоги.
func (r *PickupRefreshRunner) RunOnce(ctx context.Context) {
	for _, result := range r.refresher.Refresh(ctx) {
		carrier := string(result.Carrier)

		switch {
		case result.Skipped:
			r.runs.WithLabelValues(carrier, "skipped").Inc()
		case result.Err != nil:
			r.runs.WithLabelValues(carrier, "failed").Inc()
			r.logger.Error("пункты выдачи: обновление не удалось", slog.String("carrier", carrier),
				slog.Int("fetched", result.Fetched), slog.Any("error", result.Err))
		default:
			r.runs.WithLabelValues(carrier, "done").Inc()
			r.fetched.WithLabelValues(carrier).Set(float64(result.Fetched))
			r.logger.Info("пункты выдачи: обновлены", slog.String("carrier", carrier),
				slog.Int("fetched", result.Fetched), slog.Int64("closed", result.Deactivated))
		}
	}
}

// Run обновляет сразу и далее раз в интервал до отмены контекста.
func (r *PickupRefreshRunner) Run(ctx context.Context) error {
	for {
		r.RunOnce(ctx)

		if !wait(ctx, r.interval) {
			return nil
		}
	}
}

// SettingsSyncRunner сверяет настройки магазинов раз в интервал.
type SettingsSyncRunner struct {
	syncer   SettingsSyncer
	interval time.Duration
	logger   *slog.Logger
	failures *prometheus.CounterVec
}

// NewSettingsSyncRunner — сверка настроек с метрикой сбоев по магазину.
func NewSettingsSyncRunner(syncer SettingsSyncer, interval time.Duration, logger *slog.Logger, registerer prometheus.Registerer) *SettingsSyncRunner {
	r := &SettingsSyncRunner{
		syncer: syncer, interval: interval, logger: logger,
		failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "marketplace_shop_settings_sync_failures_total",
			Help: "Неудачные сверки настроек магазина с сервером.",
		}, []string{"tenant"}),
	}
	registerer.MustRegister(r.failures)

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
			for tenant, cause := range result.Failed {
				r.failures.WithLabelValues(string(tenant)).Inc()
				r.logger.Warn("настройки магазина: сервер не ответил", slog.String("tenantId", string(tenant)), slog.Any("error", cause))
			}

			r.logger.Info("настройки магазинов: сверены", slog.Int("checked", result.Checked), slog.Int("changed", result.Changed),
				slog.Int("failed", len(result.Failed)))
		}

		if !wait(ctx, r.interval) {
			return nil
		}
	}
}
