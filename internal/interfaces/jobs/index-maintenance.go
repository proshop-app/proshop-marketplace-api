package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// IndexMaintainer собирает отсутствующий индекс и выполняет необходимую пересборку.
type IndexMaintainer interface {
	Maintain(context.Context) (bool, error)
}

// IndexMaintenanceRunner собирает отсутствующий индекс и переносит выросший каталог
// с сохранением рабочей выдачи; старт процесса этой работы не ждёт.
type IndexMaintenanceRunner struct {
	maintainer IndexMaintainer
	interval   time.Duration
	logger     *slog.Logger
	runs       *prometheus.CounterVec
}

// NewIndexMaintenanceRunner — фоновая проверка с метриками без tenant-label.
func NewIndexMaintenanceRunner(
	maintainer IndexMaintainer,
	interval time.Duration,
	logger *slog.Logger,
	registerer prometheus.Registerer,
) *IndexMaintenanceRunner {
	runs := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "marketplace_index_maintenance_total",
			Help: "Фоновые проверки индекса: unchanged, rebuilt, busy, failed.",
		},
		[]string{"result"},
	)
	registerer.MustRegister(runs)

	return &IndexMaintenanceRunner{
		maintainer: maintainer,
		interval:   interval,
		logger:     logger,
		runs:       runs,
	}
}

// Run проверяет сразу и повторяет после паузы; отказ не останавливает выдачу и outbox.
func (r *IndexMaintenanceRunner) Run(ctx context.Context) error {
	for {
		rebuilt, err := r.maintainer.Maintain(ctx)

		switch {
		case errors.Is(err, ports.ErrRebuildBusy):
			r.runs.WithLabelValues("busy").Inc()
		case err != nil:
			if ctx.Err() == nil {
				r.runs.WithLabelValues("failed").Inc()
				r.logger.Error("перенос поискового каталога отложен", slog.Any("error", err))
			}
		case rebuilt:
			r.runs.WithLabelValues("rebuilt").Inc()
			r.logger.Info("поисковый каталог перераспределён")
		default:
			r.runs.WithLabelValues("unchanged").Inc()
		}

		if !wait(ctx, r.interval) {
			return nil
		}
	}
}
