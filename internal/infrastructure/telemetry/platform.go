package telemetry

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Platform учитывает вызовы ячейки и возвраты устаревших значений без меток покупателя.
type Platform struct {
	calls    *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewPlatform регистрирует метрики защиты сервера магазинов.
func NewPlatform(registerer prometheus.Registerer) *Platform {
	p := &Platform{
		calls: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_platform_calls_total",
				Help: "Исходы обращений к серверу магазинов и выдачи устаревших значений.",
			},
			[]string{"operation", "outcome"},
		),
		duration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "marketplace_platform_call_duration_seconds",
				Help:    "Длительность обращения к серверу магазинов с учётом немедленных отказов.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"operation", "outcome"},
		),
	}
	registerer.MustRegister(p.calls, p.duration)
	return p
}

// PlatformCall наблюдает фиксированный набор операций и исходов.
func (p *Platform) PlatformCall(operation, outcome string, elapsed time.Duration) {
	p.calls.WithLabelValues(operation, outcome).Inc()
	p.duration.WithLabelValues(operation, outcome).Observe(elapsed.Seconds())
}
