package jobs

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CheckoutMetrics отражает бизнес-переходы, а не количество повторов транзакции.
type CheckoutMetrics struct {
	logger *slog.Logger
	events *prometheus.CounterVec
}

// NewCheckoutMetrics регистрирует счётчик бизнес-переходов.
func NewCheckoutMetrics(logger *slog.Logger, registerer prometheus.Registerer) *CheckoutMetrics {
	events := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "marketplace_checkout_events_total",
			Help: "Сохранённые переходы создания и отмены по магазину.",
		},
		[]string{"tenant", "event"},
	)
	registerer.MustRegister(events)
	return &CheckoutMetrics{logger: logger, events: events}
}

// RecordCheckoutEvent пишет событие с обязательной меткой магазина.
func (m *CheckoutMetrics) RecordCheckoutEvent(tenant ids.TenantID, id ids.UUID, event string) {
	m.events.WithLabelValues(string(tenant), event).Inc()
	m.logger.Info(
		"оформление: переход сохранён",
		slog.String("tenantId", string(tenant)),
		slog.String("checkoutId", string(id)),
		slog.String("event", event),
	)
}
