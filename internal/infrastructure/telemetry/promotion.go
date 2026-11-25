package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
)

// Promotion отделяет метрики рекламы от органического ранжирования.
type Promotion struct {
	events      *prometheus.CounterVec
	transitions *prometheus.CounterVec
}

// NewPromotion регистрирует счётчики продвижения отдельно от органического ранжирования.
func NewPromotion(registerer prometheus.Registerer) *Promotion {
	p := &Promotion{
		events: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_promotion_events_total",
				Help: "Учтённые показы и клики продвижения.",
			},
			[]string{"placement", "tenantId", "kind"},
		),
		transitions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_promotion_transitions_total",
				Help: "Переходы размещений после фиксации.",
			},
			[]string{"tenantId", "from", "to"},
		),
	}
	registerer.MustRegister(p.events, p.transitions)
	return p
}

// PromotionEvent увеличивает метрику только после фиксации маячка.
func (p *Promotion) PromotionEvent(s promotion.CampaignState, kind string) {
	p.events.WithLabelValues(s.PlacementCode, string(s.TenantID), kind).Inc()
}

// PromotionTransition учитывает изменение статуса после фиксации транзакции.
func (p *Promotion) PromotionTransition(s promotion.CampaignState, before promotion.Status) {
	if before != s.Status {
		p.transitions.WithLabelValues(string(s.TenantID), string(before), string(s.Status)).Inc()
	}
}
