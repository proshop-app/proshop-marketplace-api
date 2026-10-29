package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Envelope — сообщение с коротким именем топика и идентификатором для дедупа.
type Envelope struct {
	Topic   string
	EventID string
	Message kafkago.Message
}

// Projections — приём проекций каталога.
type Projections interface {
	ApplyProduct(ctx context.Context, event appcatalog.ProductEvent) error
	ApplyStock(ctx context.Context, event appcatalog.StockEvent) error
	ApplyPrice(ctx context.Context, event appcatalog.PriceEvent) error
}

// Registry — приём событий реестра магазинов.
type Registry interface {
	Apply(ctx context.Context, event appparticipant.RegistryEvent) error
}

type handler func(ctx context.Context, envelope Envelope) error

// Router разбирает сообщение по топику и вызывает сценарий приложения.
type Router struct {
	handlers map[string]handler
}

// NewRouter — обработчики всех топиков маркетплейса.
func NewRouter(projections Projections, registry Registry) *Router {
	r := &Router{handlers: map[string]handler{
		TopicProductProjected:    bind(projections, productProjected),
		TopicStockItemUpdated:    bind(projections, stockItemUpdated),
		TopicProductPriceUpdated: bind(projections, priceUpdated),
	}}

	for _, topic := range tenantTopics {
		r.handlers[topic] = bind(registry, tenantEvent)
	}

	return r
}

// bind привязывает обработчик к сценарию, который он вызывает.
func bind[T any](target T, fn func(ctx context.Context, target T, envelope Envelope) error) handler {
	return func(ctx context.Context, envelope Envelope) error {
		return fn(ctx, target, envelope)
	}
}

// Topics — топики, которые читает маркетплейс, без префикса.
func (r *Router) Topics() []string {
	topics := make([]string, 0, len(r.handlers))
	for topic := range r.handlers {
		topics = append(topics, topic)
	}

	return topics
}

// Handle обрабатывает сообщение.
func (r *Router) Handle(ctx context.Context, envelope Envelope) error {
	h, ok := r.handlers[envelope.Topic]
	if !ok {
		return ErrUnknownTopic.WithMessage("для топика нет обработчика: " + envelope.Topic)
	}

	return h(ctx, envelope)
}

// TenantHint — магазин сообщения для меток журналов и метрик: заголовок или
// ключ проекции. Согласованность источников не проверяет; значение не по
// формату идентификатора отбрасывается, чтобы повреждённые сообщения не
// плодили метки.
func TenantHint(message kafkago.Message) string {
	candidate := tenantHeader(message)

	if candidate == "" {
		var key []string
		if json.Unmarshal(message.Key, &key) == nil && len(key) == 2 {
			candidate = key[0]
		}
	}

	if _, err := ids.ParseTenantID(candidate); err != nil {
		return ""
	}

	return candidate
}

// tenantHeader — значение заголовка x-tenant; имя без учёта регистра.
func tenantHeader(message kafkago.Message) string {
	for _, h := range message.Headers {
		if strings.EqualFold(h.Key, HeaderTenant) {
			return string(h.Value)
		}
	}

	return ""
}

// resolveTenant сводит магазин из всех источников сообщения: пустой
// источник пропускается, несовпадающие — ошибка, ни одного — ErrTenantMissing.
func resolveTenant(sources ...string) (ids.TenantID, error) {
	var found string

	for _, source := range sources {
		switch {
		case source == "":
			continue
		case found == "":
			found = source
		case found != source:
			return "", ErrTenantMismatch
		}
	}

	if found == "" {
		return "", ErrTenantMissing
	}

	tenant, err := ids.ParseTenantID(found)
	if err != nil {
		return "", ErrInvalidPayload.Wrap(err)
	}

	return tenant, nil
}

func decode(message kafkago.Message, out any) error {
	decoder := json.NewDecoder(strings.NewReader(string(message.Value)))
	decoder.UseNumber()

	if err := decoder.Decode(out); err != nil {
		return ErrInvalidPayload.Wrap(err)
	}

	return nil
}

func projectionKey(message kafkago.Message) (string, string, error) {
	var key []string
	if err := json.Unmarshal(message.Key, &key); err != nil || len(key) != 2 {
		return "", "", ErrInvalidPayload.WithMessage("ключ проекции — JSON-массив [tenantId, id]")
	}

	return key[0], key[1], nil
}

func eventTime(message kafkago.Message) (time.Time, error) {
	if message.Time.IsZero() {
		return time.Time{}, ErrInvalidPayload.WithMessage("у сообщения нет времени публикации")
	}

	return message.Time.UTC().Truncate(time.Microsecond), nil
}

func rubles(value json.Number) (money.Money, error) {
	amount, err := money.ParseRubles(value.String())
	if err != nil {
		return money.Money{}, ErrInvalidPayload.Wrap(err)
	}

	return amount, nil
}

func quantity(value *json.Number) (int, error) {
	if value == nil {
		return 0, nil
	}

	number, err := value.Float64()
	if err != nil || number != math.Trunc(number) || math.Abs(number) > math.MaxInt32 {
		return 0, ErrInvalidPayload.WithMessage(fmt.Sprintf("количество не целое: %s", value.String()))
	}

	return int(number), nil
}
