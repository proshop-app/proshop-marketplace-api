package kafka

import (
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// ConsumerMetrics объединяет счётчики всех групп приёма процесса.
type ConsumerMetrics struct {
	messages *prometheus.CounterVec
	parked   *prometheus.CounterVec
	failures *prometheus.CounterVec
	panics   *prometheus.CounterVec
	lag      *prometheus.GaugeVec
	// batchSize — снимков в одной транзакции пачки; batchFallbacks — пачки, откатившиеся целиком.
	batchSize      *prometheus.HistogramVec
	batchFallbacks *prometheus.CounterVec
}

// LagMetrics пишет отставание приёма с коротким именем топика, как остальные счётчики приёма.
type LagMetrics struct {
	gauge  *prometheus.GaugeVec
	prefix string
}

// NewConsumerMetrics регистрирует общие счётчики один раз.
func NewConsumerMetrics(registerer prometheus.Registerer) *ConsumerMetrics {
	m := &ConsumerMetrics{
		messages: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_kafka_messages_total",
				Help: "Сообщения Kafka по итогу: processed — обработано, parked — в отравленных.",
			},
			[]string{"topic", "result"},
		),
		parked: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_kafka_parked_total",
				Help: "Отравленные сообщения по причине.",
			},
			[]string{"topic", "reason"},
		),
		failures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_kafka_handler_failures_total",
				Help: "Неудачные попытки обработки, после которых сообщение повторяется.",
			},
			[]string{"topic"},
		),
		panics: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_kafka_handler_panics_total",
				Help: "Паники обработчиков; сообщение повторяется и после исчерпания попыток уходит в отравленные.",
			},
			[]string{"topic"},
		),
		lag: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "marketplace_kafka_lag_messages",
				Help: "Сообщения партиции, которые группа ещё не обработала: конец журнала минус позиция группы.",
			},
			[]string{"topic", "partition"},
		),
		batchSize: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "marketplace_kafka_projection_batch_size",
				Help:    "Снимки товаров, применённые одной транзакцией пачки.",
				Buckets: []float64{1, 2, 5, 10, 20, 30, 40, 50},
			},
			[]string{"topic"},
		),
		batchFallbacks: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_kafka_projection_batch_fallbacks_total",
				Help: "Пачки снимков, откатившиеся целиком; их сообщения повторены по одному.",
			},
			[]string{"topic"},
		),
	}
	registerer.MustRegister(m.messages, m.parked, m.failures, m.panics, m.lag, m.batchSize, m.batchFallbacks)

	return m
}

// Lag — получатель отставания читателя группы для топиков установки с префиксом.
func (m *ConsumerMetrics) Lag(topicPrefix string) *LagMetrics {
	return &LagMetrics{gauge: m.lag, prefix: prefixOf(topicPrefix)}
}

// ObserveLag обновляет отставание партиции.
func (l *LagMetrics) ObserveLag(topic string, partition int, lag int64) {
	l.gauge.WithLabelValues(strings.TrimPrefix(topic, l.prefix), strconv.Itoa(partition)).Set(float64(lag))
}

// ForgetLag убирает партицию, ушедшую из назначения: её отставание считает другой процесс.
func (l *LagMetrics) ForgetLag(topic string, partition int) {
	l.gauge.DeleteLabelValues(strings.TrimPrefix(topic, l.prefix), strconv.Itoa(partition))
}
