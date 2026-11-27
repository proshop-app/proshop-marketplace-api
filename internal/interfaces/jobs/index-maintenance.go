package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// IndexIdleFactor задаёт более редкую сверку отказа без записей в индекс.
const IndexIdleFactor = 5

// IndexMaintainer собирает отсутствующий индекс и выполняет необходимую пересборку.
type IndexMaintainer interface {
	Maintain(context.Context) (bool, error)
}

// IndexMaintenanceRunner собирает отсутствующий индекс и переносит выросший каталог
// с сохранением рабочей выдачи; старт процесса этой работы не ждёт.
type IndexMaintenanceRunner struct {
	maintainer IndexMaintainer
	schedule   ports.IntervalWork
	watch      ports.WorkWatch
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

// WithSchedule назначает общее расписание и редкую проверку внешнего отказа в простое.
func (r *IndexMaintenanceRunner) WithSchedule(
	schedule ports.IntervalWork, watch ports.WorkWatch,
) *IndexMaintenanceRunner {
	r.schedule, r.watch = schedule, watch
	return r
}

// Run проверяет сразу и повторяет после паузы; отказ не останавливает выдачу и outbox.
func (r *IndexMaintenanceRunner) Run(ctx context.Context) error {
	ticker := time.NewTicker(IndexIdleFactor * r.interval)
	defer ticker.Stop()
	var active <-chan time.Time
	var changed <-chan struct{}
	if r.watch != nil {
		changed = r.watch.Changed()
	}
	if r.check(ctx) {
		active = time.After(r.interval)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
			if _, changed := r.watch.Take()[ports.IndexMaintenanceWork]; changed && active == nil {
				active = time.After(r.interval)
			}
		case <-active:
			active = nil
			if r.check(ctx) {
				active = time.After(r.interval)
			}
		case <-ticker.C:
			if r.check(ctx) && active == nil {
				active = time.After(r.interval)
			}
		}
	}
}

func (r *IndexMaintenanceRunner) check(ctx context.Context) bool {
	var rebuilt bool
	run := func(ctx context.Context) error {
		var err error
		rebuilt, err = r.maintainer.Maintain(ctx)
		return err
	}
	var err error
	if r.schedule == nil {
		err = run(ctx)
	} else {
		err = r.schedule.Run(ctx, run)
	}
	switch {
	case errors.Is(err, ports.ErrIntervalTaken), errors.Is(err, ports.ErrRebuildBusy):
		r.runs.WithLabelValues("busy").Inc()
	case err != nil:
		if ctx.Err() == nil {
			r.runs.WithLabelValues("failed").Inc()
			r.logger.Error("перенос поискового каталога отложен", slog.Any("error", err))
		}
		return true
	case rebuilt:
		r.runs.WithLabelValues("rebuilt").Inc()
		r.logger.Info("поисковый каталог перераспределён")
	default:
		r.runs.WithLabelValues("unchanged").Inc()
	}
	return false
}
