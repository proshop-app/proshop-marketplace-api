package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// OutboxReadyInterval — как часто снимать готовую работу outbox: полосы меняются медленнее разбора.
const OutboxReadyInterval = 15 * time.Second

// ReadyLanes — готовая работа outbox по видам.
type ReadyLanes interface {
	ReadyLanes(context.Context) ([]ports.ReadyLane, error)
}

// OutboxReadyRunner снимает готовую работу одним исполнителем на установку: обход всех полос не
// умножается на число реплик. Реплика без владения сбрасывает свои значения, чтобы не отдавать
// устаревшие.
type OutboxReadyRunner struct {
	source   ReadyLanes
	schedule ports.IntervalWork
	logger   *slog.Logger
	tenants  *prometheus.GaugeVec
	age      *prometheus.GaugeVec
	errors   prometheus.Counter
}

// NewOutboxReadyRunner — метрики готовой работы outbox под общим расписанием реплик.
func NewOutboxReadyRunner(
	source ReadyLanes,
	schedule ports.IntervalWork,
	logger *slog.Logger,
	registerer prometheus.Registerer,
) *OutboxReadyRunner {
	r := &OutboxReadyRunner{
		source:   source,
		schedule: schedule,
		logger:   logger,
		tenants: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_outbox_ready_tenants",
				Help: "Магазины с созревшей задачей outbox по виду, без действующих аренд и отложенных повторов.",
			},
			[]string{"kind"},
		),
		age: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_outbox_ready_age_seconds",
				Help: "Сколько ждёт старейшая созревшая задача outbox по виду.",
			},
			[]string{"kind"},
		),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "marketplace_outbox_ready_errors_total",
			Help: "Сбои снимка готовой работы outbox.",
		}),
	}
	registerer.MustRegister(r.tenants, r.age, r.errors)
	return r
}

// Run снимает готовую работу раз в OutboxReadyInterval до отмены контекста.
func (r *OutboxReadyRunner) Run(ctx context.Context) error {
	for {
		r.RunOnce(ctx)
		if !wait(ctx, OutboxReadyInterval) {
			return nil
		}
	}
}

// RunOnce обновляет метрики у владельца интервала и сбрасывает их у остальных реплик.
func (r *OutboxReadyRunner) RunOnce(ctx context.Context) {
	err := r.schedule.Run(ctx, r.record)
	switch {
	case errors.Is(err, ports.ErrIntervalTaken):
		r.tenants.Reset()
		r.age.Reset()
	case err != nil && ctx.Err() == nil:
		r.errors.Inc()
		r.logger.Error("outbox: снимок готовой работы не удался", slog.Any("error", err))
	}
}

func (r *OutboxReadyRunner) record(ctx context.Context) error {
	lanes, err := r.source.ReadyLanes(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	r.tenants.Reset()
	r.age.Reset()
	for _, lane := range lanes {
		r.tenants.WithLabelValues(lane.Kind).Set(float64(lane.Tenants))
		age := 0.0
		if lane.Tenants > 0 {
			age = max(0, now.Sub(lane.Oldest).Seconds())
		}
		r.age.WithLabelValues(lane.Kind).Set(age)
	}
	return nil
}
