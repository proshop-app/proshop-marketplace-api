package kafka

import "github.com/prometheus/client_golang/prometheus"

// ConsumerMetrics объединяет счётчики всех групп приёма процесса.
type ConsumerMetrics struct {
	messages *prometheus.CounterVec
	parked   *prometheus.CounterVec
	failures *prometheus.CounterVec
}

// NewConsumerMetrics регистрирует общие счётчики один раз.
func NewConsumerMetrics(registerer prometheus.Registerer) *ConsumerMetrics {
	m := &ConsumerMetrics{
		messages: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_kafka_messages_total",
				Help: "Сообщения Kafka по итогу: processed — обработано, parked — в отравленных.",
			},
			[]string{"topic", "tenant", "result"},
		),
		parked: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_kafka_parked_total",
				Help: "Отравленные сообщения по причине.",
			},
			[]string{"topic", "tenant", "reason"},
		),
		failures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_kafka_handler_failures_total",
				Help: "Неудачные попытки обработки, после которых сообщение повторяется.",
			},
			[]string{"topic", "tenant"},
		),
	}
	registerer.MustRegister(m.messages, m.parked, m.failures)

	return m
}
