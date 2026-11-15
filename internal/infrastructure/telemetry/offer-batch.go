package telemetry

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// OfferBatches учитывает отправки, включая дробление после постоянного отказа.
type OfferBatches struct {
	size     *prometheus.HistogramVec
	duration *prometheus.HistogramVec
}

// NewOfferBatches регистрирует размер и длительность отправки в индекс.
func NewOfferBatches(registerer prometheus.Registerer) *OfferBatches {
	observer := &OfferBatches{
		size: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "marketplace_offer_index_batch_size",
				Help:    "Число документов в вызове индекса предложений.",
				Buckets: []float64{1, 5, 10, 20, 50, 100, 150, 200},
			},
			[]string{"result"},
		),
		duration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "marketplace_offer_index_send_seconds",
				Help:    "Длительность вызова индекса, включая ожидание видимости и транспортное дробление.",
				Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60},
			},
			[]string{"result"},
		),
	}
	registerer.MustRegister(observer.size, observer.duration)
	return observer
}

// ObserveBatch отмечает успех, постоянный отказ или временный сбой.
func (o *OfferBatches) ObserveBatch(size int, duration time.Duration, err error) {
	result := "done"
	if err != nil {
		result = "retry"
		if errs.CodeOf(err) == errs.Invalid {
			result = "invalid"
		}
	}
	o.size.WithLabelValues(result).Observe(float64(size))
	o.duration.WithLabelValues(result).Observe(duration.Seconds())
}
