package telemetry

import "github.com/prometheus/client_golang/prometheus"

// OfferIndexSkips учитывает пропуски документов при фоновых обходах.
type OfferIndexSkips struct {
	total *prometheus.CounterVec
}

// NewOfferIndexSkips регистрирует счётчик по этапам индексации.
func NewOfferIndexSkips(registerer prometheus.Registerer) *OfferIndexSkips {
	total := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "marketplace_offer_index_skipped_total",
			Help: "Число документов, отклонённых при обходе индекса.",
		},
		[]string{"operation"},
	)
	registerer.MustRegister(total)
	return &OfferIndexSkips{total: total}
}

// ObserveIndexSkips добавляет отказы после подтверждения страницы.
func (o *OfferIndexSkips) ObserveIndexSkips(phase string, count int) {
	o.total.WithLabelValues(phase).Add(float64(count))
}
