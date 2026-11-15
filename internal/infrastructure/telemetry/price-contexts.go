package telemetry

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// PriceContexts считает работу кэша и фоновых загрузок без идентификаторов покупателей.
type PriceContexts struct {
	invalidations *prometheus.CounterVec
	events        *prometheus.CounterVec
	duration      *prometheus.HistogramVec
	size          *prometheus.GaugeVec
}

// NewPriceContexts регистрирует метрики кэша и загрузок.
func NewPriceContexts(registerer prometheus.Registerer) *PriceContexts {
	p := &PriceContexts{
		invalidations: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_price_context_invalidations_total",
				Help: "Сбросы ценовых контекстов по событиям продавца.",
			},
			[]string{"tenant", "outcome"},
		),
		events: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_price_contexts_total",
				Help: "Попадания, промахи, устаревание, публичные ответы, загрузки и сбросы контекстов.",
			},
			[]string{"operation", "outcome"},
		),
		duration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "marketplace_price_context_load_duration_seconds",
				Help:    "Длительность фоновой загрузки полного снимка покупателя.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"outcome"},
		),
		size: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_price_context_cache_size",
				Help: "Число записей кэша контекстов при последнем обслуживании.",
			},
			[]string{"backend"},
		),
	}
	registerer.MustRegister(p.events, p.duration, p.size, p.invalidations)
	return p
}

// PriceContext учитывает исход, для загрузки — также длительность.
func (p *PriceContexts) PriceContext(operation, outcome string, elapsed time.Duration) {
	p.events.WithLabelValues(operation, outcome).Inc()
	if operation == "load" && outcome != "skipped" {
		p.duration.WithLabelValues(outcome).Observe(elapsed.Seconds())
	}
}

// PriceContextSize обновляет число записей указанного хранилища.
func (p *PriceContexts) PriceContextSize(backend string, size int) {
	p.size.WithLabelValues(backend).Set(float64(size))
}

// PriceContextInvalidation учитывает исход события с меткой продавца.
func (p *PriceContexts) PriceContextInvalidation(tenant ids.TenantID, outcome string) {
	p.invalidations.WithLabelValues(string(tenant), outcome).Inc()
}
