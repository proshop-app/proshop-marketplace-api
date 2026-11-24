package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
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

// processBatch доводит до записанного итога пачку снимков разных товаров одной транзакцией.
// Неразобранные сообщения пачки и вся пачка после отката идут обычным путём по одному:
// повтор с паузой или отравленные, как без пачки. Ошибка — только отмена контекста.
func (c *Consumer) processBatch(ctx context.Context, batch []*delivery) error {
	if len(batch) == 1 {
		return c.Process(ctx, batch[0].message)
	}
	envelopes := make([]Envelope, 0, len(batch))
	for _, item := range batch {
		envelopes = append(envelopes, c.envelope(item.message))
	}
	var rejected []int
	err := errs.Guard(func() error {
		var err error
		rejected, err = c.router.HandleProducts(ctx, envelopes)
		return err
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		c.metrics.batchFallbacks.WithLabelValues(envelopes[0].Topic).Inc()
		c.logger.Warn(
			"kafka: пачка снимков откатилась, сообщения по одному",
			slog.Int("messages", len(batch)),
			slog.Any("error", err),
		)
		return c.processEach(ctx, batch)
	}
	c.metrics.batchSize.WithLabelValues(envelopes[0].Topic).Observe(float64(len(batch) - len(rejected)))
	failed := make([]*delivery, 0, len(rejected))
	for n, item := range batch {
		if slices.Contains(rejected, n) {
			failed = append(failed, item)
			continue
		}
		c.metrics.messages.WithLabelValues(envelopes[n].Topic, "processed").Inc()
	}
	return c.processEach(ctx, failed)
}

// processEach ведёт сообщения по одному обычным путём повторов и отравленных.
func (c *Consumer) processEach(ctx context.Context, batch []*delivery) error {
	for _, item := range batch {
		if err := c.Process(ctx, item.message); err != nil {
			return err
		}
	}
	return nil
}

func (c *Consumer) envelope(message kafkago.Message) Envelope {
	return Envelope{
		Topic:   strings.TrimPrefix(message.Topic, prefixOf(c.config.TopicPrefix)),
		EventID: fmt.Sprintf("%s:%d:%d", message.Topic, message.Partition, message.Offset),
		Message: message,
	}
}

// Process доводит одно сообщение до записанного итога. Позиции фиксирует Run пачкой.
// Возвращает ошибку только при отмене контекста.
func (c *Consumer) Process(ctx context.Context, message kafkago.Message) error {
	envelope := c.envelope(message)
	var log *slog.Logger
	var tenantID string
	unknownFailures := 0
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.handle(ctx, envelope)
		if err == nil {
			c.metrics.messages.WithLabelValues(envelope.Topic, "processed").Inc()

			return ctx.Err()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if log == nil {
			tenantID = TenantHint(message)
			log = c.messageLogger(envelope, tenantID)
		}
		c.notePanic(log, envelope.Topic, err)
		if errs.RetryKindOf(err) == errs.RetryUnknown {
			unknownFailures++
		}
		if c.settle(ctx, log, envelope, tenantID, err, attempt, unknownFailures) {
			return ctx.Err()
		}
		if !sleep(ctx, c.config.Backoff(attempt)) {
			return ctx.Err()
		}
	}
}

// handle — единственное место перехвата паники приёма: паника на одном кривом
// сообщении становится ошибкой internal и идёт путём повторов и отравленных, а не
// роняет процесс, который после перезапуска упал бы на том же сообщении снова.
func (c *Consumer) handle(ctx context.Context, envelope Envelope) error {
	return errs.Guard(func() error { return c.router.Handle(ctx, envelope) })
}

func (c *Consumer) messageLogger(envelope Envelope, tenantID string) *slog.Logger {
	return c.logger.With(
		slog.String("topic", envelope.Topic),
		slog.Int("partition", envelope.Message.Partition),
		slog.Int64("offset", envelope.Message.Offset),
		slog.String("tenantId", tenantID),
	)
}

// notePanic пишет стек один раз на попытку: в отравленные уходит только текст ошибки.
func (c *Consumer) notePanic(log *slog.Logger, topic string, err error) {
	stack, ok := errs.PanicStack(err)
	if !ok {
		return
	}
	c.metrics.panics.WithLabelValues(topic).Inc()
	log.Error("kafka: паника обработчика", slog.Any("error", err), slog.String("stack", string(stack)))
}

// settle откладывает сообщение в отравленные, если повтор не поможет; true — итог записан.
func (c *Consumer) settle(
	ctx context.Context,
	log *slog.Logger,
	envelope Envelope,
	tenantID string,
	err error,
	attempt, unknownFailures int,
) bool {
	retry := errs.RetryKindOf(err)
	switch {
	case retry == errs.RetryPermanent:
		return c.park(ctx, log, envelope, tenantID, reasonOf(err), err, attempt)
	case retry == errs.RetryUnknown && unknownFailures >= c.config.MaxAttempts && c.config.Ready(ctx) == nil:
		return c.park(ctx, log, envelope, tenantID, ReasonExhausted, err, attempt)
	default:
		c.metrics.failures.WithLabelValues(envelope.Topic).Inc()
		log.Warn("kafka: обработка не удалась, повтор", slog.Int("attempt", attempt), slog.Any("error", err))

		return false
	}
}

func (c *Consumer) park(
	ctx context.Context,
	log *slog.Logger,
	envelope Envelope,
	tenantID, reason string,
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
		TenantID:    tenantID,
		Reason:      reason,
		Error:       cause.Error(),
		Attempts:    attempts,
		PublishedAt: message.Time,
		ParkedAt:    c.config.Now(),
	})
	if err != nil {
		c.metrics.failures.WithLabelValues(envelope.Topic).Inc()
		log.Error("kafka: парковка не удалась, повтор", slog.Any("error", err))

		return false
	}

	c.metrics.parked.WithLabelValues(envelope.Topic, reason).Inc()
	c.metrics.messages.WithLabelValues(envelope.Topic, "parked").Inc()
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
