package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
	// Run вызывает consume заново для каждого назначения; контекст отменяется при отзыве.
	// Следующее назначение начинается только после возврата consume.
	Run(ctx context.Context, consume func(context.Context) error) error
	// Fetch ограничивает число выданных сообщений отдельно для каждой партиции.
	Fetch(ctx context.Context) (kafkago.Message, error)
	// GenerationActive отличает остановку процесса от отзыва назначения.
	GenerationActive(ctx context.Context) bool
	// Release освобождает место в окне чтения после записи итога, независимо от коммита Kafka.
	Release(ctx context.Context, message kafkago.Message)
	Commit(ctx context.Context, messages ...kafkago.Message) error
}

// Parker откладывает сообщение в отравленные.
type Parker interface {
	Park(ctx context.Context, event ports.ParkedEvent) error
}

// ConsumerConfig — политика повторов.
type ConsumerConfig struct {
	// Workers — предел одновременно обрабатываемых ключей; ноль — восемь.
	Workers int
	// TopicPrefix — префикс установки в именах топиков.
	TopicPrefix string
	// MaxAttempts ограничивает только ошибки без установленной причины при живом хранилище.
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
	metrics *ConsumerMetrics
}

// NewConsumer — консьюмер с общими для групп счётчиками приёма.
func NewConsumer(
	source Source,
	router *Router,
	parker Parker,
	config ConsumerConfig,
	logger *slog.Logger,
	metrics *ConsumerMetrics,
) *Consumer {
	if config.Workers <= 0 {
		config.Workers = 8
	}

	return &Consumer{source: source, router: router, parker: parker, config: config, logger: logger, metrics: metrics}
}

// Run читает до отмены контекста.
func (c *Consumer) Run(ctx context.Context) error {
	return c.source.Run(ctx, c.runGeneration)
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

// Process доводит одно сообщение до записанного итога. Позиции фиксирует Run пачкой.
// Возвращает ошибку только при отмене контекста.
func (c *Consumer) Process(ctx context.Context, message kafkago.Message) error {
	envelope := Envelope{
		Topic:   strings.TrimPrefix(message.Topic, prefixOf(c.config.TopicPrefix)),
		EventID: fmt.Sprintf("%s:%d:%d", message.Topic, message.Partition, message.Offset),
		Message: message,
	}
	log := c.logger.With(
		slog.String("topic", envelope.Topic),
		slog.Int("partition", message.Partition),
		slog.Int64("offset", message.Offset),
	)
	// Пустая метка Prometheus эквивалентна отсутствующей. У проекции
	// серии по двум темам общие для всех магазинов.
	var tenantLabel string
	if envelope.Topic != TopicProductProjected && envelope.Topic != TopicProductProjectedBulk {
		tenantLabel = TenantHint(message)
		log = log.With(slog.String("tenantId", tenantLabel))
	}

	unknownFailures := 0
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.router.Handle(ctx, envelope)
		if err != nil && errs.RetryKindOf(err) == errs.RetryUnknown {
			unknownFailures++
		}

		switch {
		case err == nil:
			c.metrics.messages.WithLabelValues(envelope.Topic, tenantLabel, "processed").Inc()

			return ctx.Err()
		case ctx.Err() != nil:
			return ctx.Err()
		case errs.RetryKindOf(err) == errs.RetryPermanent:
			if c.park(ctx, log, envelope, tenantLabel, reasonOf(err), err, attempt) {
				return ctx.Err()
			}
		case unknownFailures >= c.config.MaxAttempts && errs.RetryKindOf(err) == errs.RetryUnknown &&
			c.config.Ready(ctx) == nil:
			if c.park(ctx, log, envelope, tenantLabel, ReasonExhausted, err, attempt) {
				return ctx.Err()
			}
		default:
			c.metrics.failures.WithLabelValues(envelope.Topic, tenantLabel).Inc()
			log.Warn("kafka: обработка не удалась, повтор", slog.Int("attempt", attempt), slog.Any("error", err))
		}

		if !sleep(ctx, c.config.Backoff(attempt)) {
			return ctx.Err()
		}
	}
}

func (c *Consumer) park(
	ctx context.Context,
	log *slog.Logger,
	envelope Envelope,
	tenantLabel, reason string,
	cause error,
	attempts int,
) bool {
	message := envelope.Message
	headers := make(map[string]string, len(message.Headers))
	for _, h := range message.Headers {
		headers[h.Key] = string(h.Value)
	}

	err := c.parker.Park(ctx, ports.ParkedEvent{
		Topic:       message.Topic,
		Partition:   message.Partition,
		Offset:      message.Offset,
		Key:         message.Key,
		Headers:     headers,
		Value:       message.Value,
		TenantID:    TenantHint(message),
		Reason:      reason,
		Error:       cause.Error(),
		Attempts:    attempts,
		PublishedAt: message.Time,
		ParkedAt:    c.config.Now(),
	})
	if err != nil {
		c.metrics.failures.WithLabelValues(envelope.Topic, tenantLabel).Inc()
		log.Error("kafka: парковка не удалась, повтор", slog.Any("error", err))

		return false
	}

	c.metrics.parked.WithLabelValues(envelope.Topic, tenantLabel, reason).Inc()
	c.metrics.messages.WithLabelValues(envelope.Topic, tenantLabel, "parked").Inc()
	log.Error("kafka: сообщение в отравленных", slog.String("reason", reason), slog.Any("error", cause))

	return true
}

// reasonOf сохраняет стабильный ключ ошибки в диагностике оператора.
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
