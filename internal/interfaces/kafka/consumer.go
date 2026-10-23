package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// Причины парковки, кроме ключей типизированных ошибок.
const (
	ReasonExhausted = "attempts_exhausted"
)

// Source — чтение группой с ручным коммитом.
type Source interface {
	Fetch(ctx context.Context) (kafkago.Message, error)
	Commit(ctx context.Context, message kafkago.Message) error
}

// Parker откладывает сообщение в отравленные.
type Parker interface {
	Park(ctx context.Context, event ports.ParkedEvent) error
}

// ConsumerConfig — политика повторов.
type ConsumerConfig struct {
	// TopicPrefix — префикс установки в именах топиков.
	TopicPrefix string
	// MaxAttempts — после стольких неудач сообщение паркуется, если хранилище живо.
	MaxAttempts int
	// Backoff — пауза перед повтором по номеру попытки.
	Backoff func(attempt int) time.Duration
	// Ready проверяет хранилище: при недоступной базе сообщение не паркуется, а ждёт.
	Ready func(ctx context.Context) error
	Now   func() time.Time
}

// Consumer читает топики и коммитит позицию только после записи результата:
// обработки, дедупа или парковки. Сообщение без магазина и любое
// повреждённое сообщение уходят в отравленные с метрикой; временные сбои
// повторяются, а не теряются.
type Consumer struct {
	source  Source
	router  *Router
	parker  Parker
	config  ConsumerConfig
	logger  *slog.Logger
	metrics *metrics
}

type metrics struct {
	messages *prometheus.CounterVec
	parked   *prometheus.CounterVec
	failures *prometheus.CounterVec
}

// NewConsumer — консьюмер с метриками в registerer.
func NewConsumer(source Source, router *Router, parker Parker, config ConsumerConfig, logger *slog.Logger, registerer prometheus.Registerer) *Consumer {
	m := &metrics{
		messages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "marketplace_kafka_messages_total",
			Help: "Сообщения Kafka по итогу: processed — обработано, parked — в отравленных.",
		}, []string{"topic", "tenant", "result"}),
		parked: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "marketplace_kafka_parked_total",
			Help: "Отравленные сообщения по причине; tenant пуст, если магазин не определён.",
		}, []string{"topic", "tenant", "reason"}),
		failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "marketplace_kafka_handler_failures_total",
			Help: "Неудачные попытки обработки, после которых сообщение повторяется.",
		}, []string{"topic", "tenant"}),
	}
	registerer.MustRegister(m.messages, m.parked, m.failures)

	return &Consumer{source: source, router: router, parker: parker, config: config, logger: logger, metrics: m}
}

// FullTopics — полные имена топиков маршрутизатора с префиксом установки:
// "<prefix>.<topic>", как их называет сервер.
func FullTopics(prefix string, router *Router) []string {
	short := router.Topics()
	full := make([]string, 0, len(short))

	for _, topic := range short {
		full = append(full, prefixOf(prefix)+topic)
	}

	return full
}

// Run читает до отмены контекста.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		message, ok := c.next(ctx)
		if !ok {
			return nil
		}

		// Process возвращает ошибку только при отмене контекста; её увидит следующее чтение
		if err := c.Process(ctx, message); err != nil && ctx.Err() == nil {
			return err
		}
	}
}

// next читает следующее сообщение, повторяя сбои чтения с паузой; false — контекст отменён.
func (c *Consumer) next(ctx context.Context) (kafkago.Message, bool) {
	for failures := 1; ; failures++ {
		message, err := c.source.Fetch(ctx)
		if err == nil {
			return message, true
		}
		if ctx.Err() != nil {
			return kafkago.Message{}, false
		}

		c.logger.Error("kafka: чтение не удалось", slog.Any("error", err))

		if !sleep(ctx, c.config.Backoff(failures)) {
			return kafkago.Message{}, false
		}
	}
}

// Process доводит одно сообщение до записанного итога и коммитит позицию.
// Возвращает ошибку только при отмене контекста.
func (c *Consumer) Process(ctx context.Context, message kafkago.Message) error {
	envelope := Envelope{
		Topic:   strings.TrimPrefix(message.Topic, prefixOf(c.config.TopicPrefix)),
		EventID: fmt.Sprintf("%s:%d:%d", message.Topic, message.Partition, message.Offset),
		Message: message,
	}
	tenant := TenantHint(message)
	log := c.logger.With(slog.String("topic", envelope.Topic), slog.String("tenantId", tenant),
		slog.Int("partition", message.Partition), slog.Int64("offset", message.Offset))

	for attempt := 1; ; attempt++ {
		err := c.router.Handle(ctx, envelope)

		switch {
		case err == nil:
			c.metrics.messages.WithLabelValues(envelope.Topic, tenant, "processed").Inc()

			return c.commit(ctx, log, message)
		case ctx.Err() != nil:
			return ctx.Err()
		case permanent(err):
			if c.park(ctx, log, envelope, tenant, reasonOf(err), err, attempt) {
				return c.commit(ctx, log, message)
			}
		case attempt >= c.config.MaxAttempts && c.config.Ready(ctx) == nil:
			if c.park(ctx, log, envelope, tenant, ReasonExhausted, err, attempt) {
				return c.commit(ctx, log, message)
			}
		default:
			c.metrics.failures.WithLabelValues(envelope.Topic, tenant).Inc()
			log.Warn("kafka: обработка не удалась, повтор", slog.Int("attempt", attempt), slog.Any("error", err))
		}

		if !sleep(ctx, c.config.Backoff(attempt)) {
			return ctx.Err()
		}
	}
}

func (c *Consumer) park(ctx context.Context, log *slog.Logger, envelope Envelope, tenant, reason string, cause error, attempts int) bool {
	message := envelope.Message
	headers := make(map[string]string, len(message.Headers))
	for _, h := range message.Headers {
		headers[h.Key] = string(h.Value)
	}

	err := c.parker.Park(ctx, ports.ParkedEvent{
		Topic: message.Topic, Partition: message.Partition, Offset: message.Offset,
		Key: message.Key, Headers: headers, Value: message.Value, TenantID: tenant,
		Reason: reason, Error: cause.Error(), Attempts: attempts, ParkedAt: c.config.Now(),
	})
	if err != nil {
		c.metrics.failures.WithLabelValues(envelope.Topic, tenant).Inc()
		log.Error("kafka: парковка не удалась, повтор", slog.Any("error", err))

		return false
	}

	c.metrics.parked.WithLabelValues(envelope.Topic, tenant, reason).Inc()
	c.metrics.messages.WithLabelValues(envelope.Topic, tenant, "parked").Inc()
	log.Error("kafka: сообщение в отравленных", slog.String("reason", reason), slog.Any("error", cause))

	return true
}

// commit фиксирует позицию. Сбой коммита не теряет данных: сообщение придёт
// снова и отсеется дедупом.
func (c *Consumer) commit(ctx context.Context, log *slog.Logger, message kafkago.Message) error {
	if err := c.source.Commit(ctx, message); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		log.Warn("kafka: коммит не удался, сообщение придёт повторно", slog.Any("error", err))
	}

	return nil
}

// permanent — повтор с тем же сообщением не поможет.
func permanent(err error) bool {
	return errs.CodeOf(err) == errs.Invalid
}

func reasonOf(err error) string {
	if typed, ok := errs.As(err); ok {
		return typed.Key()
	}

	return "unknown"
}

func prefixOf(prefix string) string {
	if prefix == "" {
		return ""
	}

	return prefix + "."
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
