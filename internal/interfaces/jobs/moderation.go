package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

// ModerationStats отделяет цикл от транзакций приложения.
type ModerationStats interface {
	OpenStats(context.Context) ([]moderation.OpenStat, error)
}

// ModerationMetrics регистрируется один раз для сценариев и фонового цикла.
type ModerationMetrics struct {
	verdicts  *prometheus.CounterVec
	decisions *prometheus.CounterVec
	open      *prometheus.GaugeVec
	oldest    prometheus.Gauge
}

// NewModerationMetrics связывает все метрики модуля с реестром процесса.
func NewModerationMetrics(registerer prometheus.Registerer) *ModerationMetrics {
	m := &ModerationMetrics{
		verdicts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_moderation_verdicts_total",
				Help: "Изменения вердиктов модерации.",
			},
			[]string{"tenant", "status", "decided_by"},
		),
		decisions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_moderation_decisions_total",
				Help: "Решения оператора по делам.",
			},
			[]string{"action"},
		),
		open: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_moderation_cases_open",
				Help: "Открытые дела по участникам.",
			},
			[]string{"tenant"},
		),
		oldest: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "marketplace_moderation_case_oldest_age_seconds",
			Help: "Возраст старейшего открытого дела.",
		}),
	}
	registerer.MustRegister(m.verdicts, m.decisions, m.open, m.oldest)
	return m
}

// Verdict учитывает только зафиксированное изменение.
func (m *ModerationMetrics) Verdict(v moderation.VerdictState) {
	m.verdicts.WithLabelValues(string(v.TenantID), string(v.Status), v.DecidedBy).Inc()
}

// Decision не учитывает повтор ответа с тем же ключом.
func (m *ModerationMetrics) Decision(action moderation.Action) {
	m.decisions.WithLabelValues(string(action)).Inc()
}

// Refresh сбрасывает исчезнувших участников, сохраняя прошлые значения при ошибке чтения.
func (m *ModerationMetrics) Refresh(ctx context.Context, service ModerationStats, now time.Time) error {
	stats, err := service.OpenStats(ctx)
	if err != nil {
		return err
	}
	m.open.Reset()
	oldest := 0.0
	for _, stat := range stats {
		m.open.WithLabelValues(string(stat.TenantID)).Set(float64(stat.Count))
		oldest = max(oldest, now.Sub(stat.Oldest).Seconds())
	}
	m.oldest.Set(oldest)
	return nil
}

// Run обновляет очередь сразу после запуска и затем по расписанию.
func (m *ModerationMetrics) Run(
	ctx context.Context,
	service ModerationStats,
	interval time.Duration,
	logger *slog.Logger,
) error {
	for {
		if err := m.Refresh(ctx, service, time.Now()); err != nil && ctx.Err() == nil {
			logger.Error("модерация: не удалось обновить метрики", slog.Any("error", err))
		}
		if !wait(ctx, interval) {
			return nil
		}
	}
}
