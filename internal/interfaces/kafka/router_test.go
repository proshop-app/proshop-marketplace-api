package kafka_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

var published = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

type recorder struct {
	products []appcatalog.ProductEvent
	tags     []appcatalog.TagEvent
	stock    []appcatalog.StockEvent
	prices   []appcatalog.PriceEvent
	registry []appparticipant.RegistryEvent
	err      error
}

const productBody = `{"version":1,"tenantId":"t1","productId":"p1","revision":7,"operation":"upsert","product":{
	"id":"p1","name":"Товар","description":"Описание","image":null,"url":"/p1","sku":"S1",
	"price":1999.9,"promoPrice":1499.995,"availableQuantity":3,
	"categories":[{"id":"c1","name":"","title":"Категория","url":"/c1"}],"tags":["t-1"],
	"marketplace":{"categoryId":"c1","attributes":{"a1":"v1","a2":1.5,"a3":false},"published":true},
	"variantsId":"v1","conditions":{"isVisible":true,"isOnlyVariant":false},"unknownField":42}}`

func (r *recorder) ApplyProduct(_ context.Context, e appcatalog.ProductEvent) error {
	r.products = append(r.products, e)

	return r.err
}

func (r *recorder) ApplyTag(_ context.Context, e appcatalog.TagEvent) error {
	r.tags = append(r.tags, e)

	return r.err
}

func (r *recorder) ApplyStock(_ context.Context, e appcatalog.StockEvent) error {
	r.stock = append(r.stock, e)

	return r.err
}

func (r *recorder) ApplyPrice(_ context.Context, e appcatalog.PriceEvent) error {
	r.prices = append(r.prices, e)

	return r.err
}

func (r *recorder) Apply(_ context.Context, e appparticipant.RegistryEvent) error {
	r.registry = append(r.registry, e)

	return r.err
}

func message(key, value string, headers ...kafkago.Header) kafkago.Message {
	return kafkago.Message{Key: []byte(key), Value: []byte(value), Headers: headers, Time: published}
}

func tenantHeader(value string) kafkago.Header {
	return kafkago.Header{Key: kafka.HeaderTenant, Value: []byte(value)}
}

func handle(t *testing.T, topic string, msg kafkago.Message) (*recorder, error) {
	t.Helper()

	rec := &recorder{}
	err := kafka.NewRouter(rec, rec).
		Handle(context.Background(), kafka.Envelope{Topic: topic, EventID: "ev", Message: msg})

	return rec, err
}

func TestProductProjected(t *testing.T) {
	rec, err := handle(t, kafka.TopicProductProjected, message(`["t1","p1"]`, productBody))
	if err != nil {
		t.Fatal(err)
	}

	got := rec.products[0]
	s := got.Snapshot
	if got.EventID != "ev" || s.TenantID != "t1" || s.ProductID != "p1" || s.Revision != 7 || s.Deleted {
		t.Fatalf("конверт: %+v", got)
	}
	if s.Price.Kopecks() != 199990 || s.PromoPrice == nil || s.PromoPrice.Kopecks() != 150000 || s.Available != 3 {
		t.Fatalf("цены в копейках и остаток: %+v", s)
	}
	if s.Card.Name != "Товар" || s.Card.Categories[0].Name != "Категория" || !s.Card.Visible ||
		s.Card.VariantsID != "v1" ||
		!s.At.Equal(published) {
		t.Fatalf("карточка: %+v", s.Card)
	}
}

func TestProductProjectedDelete(t *testing.T) {
	body := `{"version":1,"tenantId":"t1","productId":"p1","revision":8,"operation":"delete"}`

	rec, err := handle(t, kafka.TopicProductProjected, message(`["t1","p1"]`, body, tenantHeader("t1")))
	if err != nil || !rec.products[0].Snapshot.Deleted {
		t.Fatalf("удаление: %+v, %v", rec.products, err)
	}
}

func TestProductProjectedRejects(t *testing.T) {
	cases := map[string]struct {
		msg  kafkago.Message
		want error
	}{
		"нет магазина в теле": {
			message(`["t1","p1"]`, `{"version":1,"productId":"p1","revision":1,"operation":"delete"}`),
			kafka.ErrTenantMissing,
		},
		"ключ другого магазина": {message(`["t2","p1"]`, productBody), kafka.ErrTenantMismatch},
		"заголовок другого":     {message(`["t1","p1"]`, productBody, tenantHeader("t2")), kafka.ErrTenantMismatch},
		"ключ не массив":        {message(`p1`, productBody), kafka.ErrInvalidPayload},
		"другой товар в ключе":  {message(`["t1","p2"]`, productBody), kafka.ErrInvalidPayload},
		"не JSON":               {message(`["t1","p1"]`, `{`), kafka.ErrInvalidPayload},
		"версия контракта": {
			message(`["t1","p1"]`, `{"version":2,"tenantId":"t1","productId":"p1","revision":1,"operation":"delete"}`),
			kafka.ErrInvalidPayload,
		},
		"операция": {
			message(`["t1","p1"]`, `{"version":1,"tenantId":"t1","productId":"p1","revision":1,"operation":"merge"}`),
			kafka.ErrInvalidPayload,
		},
		"удаление со снимком": {
			message(`["t1","p1"]`, `{
				"version": 1, "tenantId": "t1", "productId": "p1", "revision": 1, "operation": "delete",
				"product": {"id": "p1"}
			}`),
			kafka.ErrInvalidPayload,
		},
		"цена с экспонентой": {
			message(`["t1","p1"]`, `{
				"version": 1, "tenantId": "t1", "productId": "p1", "revision": 1, "operation": "upsert",
				"product": {"id": "p1", "name": "x", "price": 1e3}
			}`),
			kafka.ErrInvalidPayload,
		},
		"без цены": {
			message(`["t1","p1"]`, `{
				"version": 1, "tenantId": "t1", "productId": "p1", "revision": 1, "operation": "upsert",
				"product": {"id": "p1", "name": "x"}
			}`),
			kafka.ErrInvalidPayload,
		},
		"дробный остаток": {
			message(`["t1","p1"]`, `{
				"version": 1, "tenantId": "t1", "productId": "p1", "revision": 1, "operation": "upsert",
				"product": {"id": "p1", "name": "x", "price": 1, "availableQuantity": 1.5}
			}`),
			kafka.ErrInvalidPayload,
		},
		"магазин не по формату": {
			message(`["t 1","p1"]`, `{
				"version": 1, "tenantId": "t 1", "productId": "p1", "revision": 1, "operation": "delete"
			}`),
			kafka.ErrInvalidPayload,
		},
	}

	for name, tc := range cases {
		rec, err := handle(t, kafka.TopicProductProjected, tc.msg)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: ожидалось %v, получено %v", name, tc.want, err)
		}
		if len(rec.products) != 0 {
			t.Fatalf("%s: сценарий не вызывается", name)
		}
	}

	noTime := message(`["t1","p1"]`, productBody)
	noTime.Time = time.Time{}
	if _, err := handle(t, kafka.TopicProductProjected, noTime); !errors.Is(err, kafka.ErrInvalidPayload) {
		t.Fatalf("без времени публикации: %v", err)
	}
}

func TestTagProjected(t *testing.T) {
	body := `{
		"version": 1, "tenantId": "t1", "tagId": "tag-1", "revision": 2, "operation": "upsert",
		"tag": {"id": "tag-1", "value": "Новинка"}
	}`

	rec, err := handle(t, kafka.TopicTagProjected, message(`["t1","tag-1"]`, body))
	if err != nil || rec.tags[0].Snapshot.Value != "Новинка" || rec.tags[0].Snapshot.TenantID != "t1" {
		t.Fatalf("тег: %+v, %v", rec.tags, err)
	}

	_, err = handle(t, kafka.TopicTagProjected, message(`["t2","tag-1"]`, body))
	if !errors.Is(err, kafka.ErrTenantMismatch) {
		t.Fatalf("ключ другого магазина: %v", err)
	}

	deleted := `{"version":1,"tenantId":"t1","tagId":"tag-1","revision":3,"operation":"delete"}`
	rec, err = handle(t, kafka.TopicTagProjected, message(`["t1","tag-1"]`, deleted))
	if err != nil || !rec.tags[0].Snapshot.Deleted {
		t.Fatalf("удаление тега: %+v, %v", rec.tags, err)
	}
}

func TestStockItemUpdated(t *testing.T) {
	body := `{"id":"s1","productId":"p1","quantity":10,"reserved":4,"price":250.5}`

	rec, err := handle(t, kafka.TopicStockItemUpdated, message("s1", body, tenantHeader("t1")))
	if err != nil {
		t.Fatal(err)
	}

	got := rec.stock[0]
	if got.TenantID != "t1" || got.ProductID != "p1" || got.Stock.Available != 6 || got.Price == nil ||
		got.Price.Price.Kopecks() != 25050 {
		t.Fatalf("позиция склада: %+v", got)
	}

	overReserved := `{"id":"s1","productId":"p1","quantity":1,"reserved":4}`
	rec, err = handle(t, kafka.TopicStockItemUpdated, message("s1", overReserved, tenantHeader("t1")))
	if err != nil || rec.stock[0].Stock.Available != 0 || rec.stock[0].Price != nil {
		t.Fatalf("резерв больше остатка — ноль, без цены: %+v, %v", rec.stock, err)
	}

	if _, err := handle(t, kafka.TopicStockItemUpdated, message("s1", body)); !errors.Is(err, kafka.ErrTenantMissing) {
		t.Fatalf("без заголовка магазина: %v", err)
	}
}

func TestPriceUpdated(t *testing.T) {
	rec, err := handle(t, kafka.TopicProductPriceUpdated, message("p1", `{"id":"p1","price":99}`, tenantHeader("t1")))
	if err != nil || rec.prices[0].Price.Price.Kopecks() != 9900 || rec.prices[0].ProductID != "p1" {
		t.Fatalf("прайс: %+v, %v", rec.prices, err)
	}

	_, err = handle(t, kafka.TopicProductPriceUpdated, message("p1", `{"id":"p1","price":99}`))
	if !errors.Is(err, kafka.ErrTenantMissing) {
		t.Fatalf("без заголовка магазина: %v", err)
	}

	_, err = handle(t, kafka.TopicProductPriceUpdated, message("p1", `{"id":"p1"}`, tenantHeader("t1")))
	if !errors.Is(err, kafka.ErrInvalidPayload) {
		t.Fatalf("без цены: %v", err)
	}
}

func TestTenantEvents(t *testing.T) {
	body := `{
		"tenantId": "t1", "slug": "shop", "status": "active", "cell": "a",
		"plan": {"code":"base"}, "hosts": [], "at": "2026-10-23T10:00:00.123Z"
	}`

	rec, err := handle(t, kafka.TopicTenantUpdated, message("t1", body))
	if err != nil {
		t.Fatal(err)
	}

	event := rec.registry[0].Event
	if event.TenantID != "t1" || event.Status != participant.TenantActive || string(event.Plan) != `{"code":"base"}` ||
		!event.At.Equal(time.Date(2026, 10, 23, 10, 0, 0, 123_000_000, time.UTC)) {
		t.Fatalf("событие реестра: %+v", event)
	}

	rec, err = handle(t, kafka.TopicTenantDeleted, message("t1", body))
	if err != nil || rec.registry[0].Event.Status != participant.TenantDeleted {
		t.Fatalf("удаление переводит в конечный статус: %+v, %v", rec.registry, err)
	}

	noTenant := `{"slug":"shop","status":"active","at":"2026-10-23T10:00:00Z"}`

	_, err = handle(t, kafka.TopicTenantCreated, message("t1", noTenant))
	if !errors.Is(err, kafka.ErrTenantMissing) {
		t.Fatalf("без магазина: %v", err)
	}

	_, err = handle(t, kafka.TopicTenantCreated, message("t1", body, tenantHeader("t2")))
	if !errors.Is(err, kafka.ErrTenantMismatch) {
		t.Fatalf("заголовок другого магазина: %v", err)
	}

	badTime := `{"tenantId":"t1","slug":"s","status":"active","at":"вчера"}`

	_, err = handle(t, kafka.TopicTenantCreated, message("t1", badTime))
	if !errors.Is(err, kafka.ErrInvalidPayload) {
		t.Fatalf("время не по формату: %v", err)
	}
}

func TestUnknownTopicAndHint(t *testing.T) {
	if _, err := handle(t, "orders.order.created", message("k", "{}")); !errors.Is(err, kafka.ErrUnknownTopic) {
		t.Fatalf("неизвестный топик: %v", err)
	}

	if hint := kafka.TenantHint(message(`["t9","p1"]`, "{}")); hint != "t9" {
		t.Fatalf("магазин из ключа проекции: %q", hint)
	}
	if hint := kafka.TenantHint(message(`x`, "{}", tenantHeader("t8"))); hint != "t8" {
		t.Fatalf("магазин из заголовка: %q", hint)
	}
	if hint := kafka.TenantHint(message(`x`, "{}")); hint != "" {
		t.Fatalf("магазина нет: %q", hint)
	}
	if hint := kafka.TenantHint(message(`x`, "{}", tenantHeader("не id {}"))); hint != "" {
		t.Fatalf("значение не по формату не становится меткой: %q", hint)
	}
}

func TestMarketplaceBlockPreservesTypes(t *testing.T) {
	rec, err := handle(t, kafka.TopicProductProjected, message(`["t1","p1"]`, productBody))
	if err != nil {
		t.Fatal(err)
	}
	card := rec.products[0].Snapshot.Card.Marketplace
	if card == nil || !card.Published || card.CategoryID != "c1" || string(card.Attributes["a2"]) != "1.5" ||
		string(card.Attributes["a3"]) != "false" {
		t.Fatalf("%+v", card)
	}
	body := strings.Replace(productBody, `"a2":1.5`, `"a2":"bad"`, 1)
	rec, err = handle(t, kafka.TopicProductProjected, message(`["t1","p1"]`, body))
	if err != nil || string(rec.products[0].Snapshot.Card.Marketplace.Attributes["a2"]) != `"bad"` {
		t.Fatalf("ошибочный атрибут должен дойти до сценария проекции: %+v %v", rec, err)
	}
}

func TestMarketplacePriceProjectedWithoutFloat(t *testing.T) {
	body := strings.Replace(productBody, `"marketplace":{`, `"marketplace":{"price":249.995,`, 1)
	rec, err := handle(t, kafka.TopicProductProjected, message(`["t1","p1"]`, body))
	if err != nil {
		t.Fatal(err)
	}
	card := rec.products[0].Snapshot.Card.Marketplace
	if card == nil || card.Price == nil || card.Price.String() != "249.995" {
		t.Fatalf("цена площадки: %+v", card)
	}
	invalid := strings.Replace(body, `"price":249.995`, `"price":-1`, 1)
	if _, err := handle(t, kafka.TopicProductProjected, message(`["t1","p1"]`, invalid)); err == nil {
		t.Fatal("отрицательная цена площадки не принимается")
	}
}
