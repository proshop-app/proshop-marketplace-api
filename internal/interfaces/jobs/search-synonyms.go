package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/synonyms"
)

// ReferenceSynonymsRunner синхронизирует фоновые проходы реплик через общую аренду.
type ReferenceSynonymsRunner struct {
	service  *synonyms.Service
	schedule ports.IndexRebuildLock
	interval time.Duration
	logger   *slog.Logger
	results  *prometheus.CounterVec
	active   prometheus.Gauge
	queued   prometheus.Gauge
}

// NewReferenceSynonymsRunner создаёт метрики без идентификаторов пар и поисковых терминов.
func NewReferenceSynonymsRunner(
	service *synonyms.Service,
	schedule ports.IndexRebuildLock,
	interval time.Duration,
	logger *slog.Logger,
	registerer prometheus.Registerer,
) *ReferenceSynonymsRunner {
	r := &ReferenceSynonymsRunner{
		service:  service,
		schedule: schedule,
		interval: interval,
		logger:   logger,
		results: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_search_synonym_checks_total",
				Help: "Сохранённые проверки направлений по результату.",
			},
			[]string{"result"},
		),
		active: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "marketplace_search_synonym_active",
			Help: "Число активных направлений после фоновой сверки.",
		}),
		queued: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "marketplace_search_synonym_quarantined",
			Help: "Число живых номинаций после фоновой сверки.",
		}),
	}
	registerer.MustRegister(r.results, r.active, r.queued)
	return r
}

// Run пытается выполнить проход сразу и затем по общему расписанию.
func (r *ReferenceSynonymsRunner) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		err := r.schedule.WithLock(ctx, r.run)
		if err != nil && !errors.Is(err, ports.ErrRebuildBusy) && ctx.Err() == nil {
			r.logger.Warn("поисковые синонимы: неполная сверка", slog.Any("error", err))
		}
		if !wait(ctx, r.interval) {
			break
		}
	}
	return nil
}

func (r *ReferenceSynonymsRunner) run(ctx context.Context) error {
	result, err := r.service.Run(ctx)
	r.results.WithLabelValues("pass").Add(float64(result.Pass))
	r.results.WithLabelValues("reject").Add(float64(result.Reject))
	r.results.WithLabelValues("retry").Add(float64(result.Retry))
	if result.LimitReached {
		r.logger.Warn("поисковые синонимы: достигнут предел очереди, источников или снимка")
	}
	if err == nil {
		r.active.Set(float64(result.Active))
		r.queued.Set(float64(result.Queued))
	}
	return err
}
