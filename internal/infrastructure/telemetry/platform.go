package telemetry

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Platform учитывает вызовы ячейки и возвраты устаревших значений без меток покупателя.
type Platform struct {
	calls      *prometheus.CounterVec
	duration   *prometheus.HistogramVec
	wait       *prometheus.HistogramVec
	rejections *prometheus.CounterVec
}

// NewPlatform регистрирует метрики защиты сервера магазинов.
func NewPlatform(registerer prometheus.Registerer) *Platform {
	p := &Platform{
		calls: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_platform_calls_total",
				Help: "Исходы обращений к серверу магазинов и выдачи устаревших значений.",
			},
			[]string{"operation", "class", "outcome"},
		),
		duration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "marketplace_platform_call_duration_seconds",
				Help:    "Длительность обращения с учётом очереди и отказов.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"operation", "class", "outcome"},
		),
		wait: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "marketplace_platform_queue_wait_seconds",
				Help:    "Ожидание места в очереди по классу и исходу допуска.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"class", "outcome"},
		),
		rejections: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_platform_rejections_total",
				Help: "Отказы по классу операции и причине.",
			},
			[]string{"class", "reason"},
		),
	}
	registerer.MustRegister(p.calls, p.duration, p.wait, p.rejections)
	return p
}

// PlatformCall наблюдает фиксированный набор операций и исходов.
func (p *Platform) PlatformCall(operation, class, outcome string, elapsed time.Duration) {
	p.calls.WithLabelValues(operation, class, outcome).Inc()
	p.duration.WithLabelValues(operation, class, outcome).Observe(elapsed.Seconds())
	switch outcome {
	case "queue_full", "queue_timeout", "timeout", "tenant_breaker", "server_breaker", "canceled":
		p.rejections.WithLabelValues(class, outcome).Inc()
	}
}

// PlatformQueueWait учитывает только вызовы, которым пришлось ждать места.
func (p *Platform) PlatformQueueWait(class, outcome string, elapsed time.Duration) {
	p.wait.WithLabelValues(class, outcome).Observe(elapsed.Seconds())
}
