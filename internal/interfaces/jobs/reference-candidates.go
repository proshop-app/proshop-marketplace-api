package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

// ReferenceCandidatesRunner пополняет открытые словари наблюдениями каталога.
type ReferenceCandidatesRunner struct {
	service  *appreference.Service
	search   ports.ReferenceCandidates
	interval time.Duration
	logger   *slog.Logger
	created  *prometheus.CounterVec
	failures prometheus.Counter
}

// NewReferenceCandidatesRunner регистрирует метрики сбора кандидатов.
func NewReferenceCandidatesRunner(
	service *appreference.Service,
	search ports.ReferenceCandidates,
	interval time.Duration,
	logger *slog.Logger,
	registerer prometheus.Registerer,
) *ReferenceCandidatesRunner {
	r := &ReferenceCandidatesRunner{
		service:  service,
		search:   search,
		interval: interval,
		logger:   logger,
		created: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_reference_candidates_created_total",
				Help: "Созданные кандидаты из каталога.",
			},
			[]string{"dictionary"},
		),
		failures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "marketplace_reference_candidates_failures_total",
			Help: "Сбои сбора кандидатов из каталога.",
		}),
	}
	registerer.MustRegister(r.created, r.failures)
	return r
}

// RunOnce продолжает обход остальных словарей после ошибки страницы.
func (r *ReferenceCandidatesRunner) RunOnce(ctx context.Context) {
	dictionaries, err := r.service.Dictionaries(ctx)
	if err != nil {
		r.failed("", err)
		return
	}
	for _, dict := range dictionaries {
		if dict.Kind != reference.KindOpen {
			continue
		}
		count, err := r.service.CollectCandidates(ctx, dict.Code, r.search)
		r.created.WithLabelValues(dict.Code).Add(float64(count))
		if err != nil {
			r.failed(dict.Code, err)
		}
	}
}

// Run выполняет сбор сразу и раз в интервал; нулевой интервал выключает его.
func (r *ReferenceCandidatesRunner) Run(ctx context.Context) error {
	if r.interval <= 0 {
		return nil
	}
	for {
		r.RunOnce(ctx)
		if !wait(ctx, r.interval) {
			return nil
		}
	}
}
