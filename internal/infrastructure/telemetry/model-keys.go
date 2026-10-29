package telemetry

import "github.com/prometheus/client_golang/prometheus"

// ModelKeys учитывает ошибки вычисления ключей модели.
type ModelKeys struct {
	failures  prometheus.Counter
	conflicts prometheus.Counter
}

// NewModelKeys регистрирует счётчик ошибок разрешения бренда.
func NewModelKeys(registerer prometheus.Registerer) *ModelKeys {
	failures := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "marketplace_model_key_resolve_failures_total",
		Help: "Отказы реестра при расчёте ключа модели.",
	})
	conflicts := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "marketplace_model_key_conflicts_total",
		Help: "Конфликты версии при расчёте ключа модели.",
	})
	registerer.MustRegister(failures, conflicts)

	return &ModelKeys{
		failures:  failures,
		conflicts: conflicts,
	}
}

// ResolveFailed учитывает сбой разрешения бренда.
func (m *ModelKeys) ResolveFailed() {
	m.failures.Inc()
}

// Conflict учитывает пропущенную из-за параллельного события запись.
func (m *ModelKeys) Conflict() {
	m.conflicts.Inc()
}
