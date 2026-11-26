package jobs

import (
	"context"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/requestid"
)

// CheckoutMetrics отражает бизнес-переходы, а не количество повторов транзакции.
type CheckoutMetrics struct {
	logger     *slog.Logger
	events     *prometheus.CounterVec
	unreadable *prometheus.CounterVec
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
	unreadable := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "marketplace_checkout_unreadable_total",
			Help: "Непрочитанные покупки по первому затронутому магазину.",
		},
		[]string{"tenant"},
	)
	registerer.MustRegister(events, unreadable)
	return &CheckoutMetrics{logger: logger, events: events, unreadable: unreadable}
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

// RecordCheckoutReadFailure считает покупку и пишет причину в пределах лимита запроса.
func (m *CheckoutMetrics) RecordCheckoutReadFailure(
	ctx context.Context,
	tenant ids.TenantID,
	id ids.UUID,
	cause error,
	log bool,
) {
	m.unreadable.WithLabelValues(string(tenant)).Inc()
	if !log {
		return
	}
	logger := m.logger
	if requestID := requestid.From(ctx); requestID != "" {
		logger = logger.With(slog.String("requestId", requestID))
	}
	logger.ErrorContext(
		ctx,
		"оформление: не удалось прочитать покупку",
		slog.String("tenantId", string(tenant)),
		slog.String("checkoutId", string(id)),
		slog.Any("error", cause),
	)
}
