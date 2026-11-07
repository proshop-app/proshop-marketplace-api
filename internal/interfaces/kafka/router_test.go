package kafka_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

var published = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

type recorder struct {
	products []appcatalog.ProductEvent
	stock    []appcatalog.StockEvent
	prices   []appcatalog.PriceEvent
	registry []appparticipant.RegistryEvent
	err      error
}

const productBody = `{"version":1,"tenantId":"t1","productId":"p1","revision":7,"operation":"upsert","product":{
	"id":"p1","name":"Товар","description":"Описание","image":null,"url":"/p1","sku":"S1",
	"price":1999.9,"promoPrice":1499.995,"availableQuantity":3,"salesCount":9007199254740993,
	"averageRating":4.7,"ratingsCount":12,
	"categories":[{"id":"c1","name":"","title":"Категория","url":"/c1"}],"tags":["t-1"],
	"marketplace":{"categoryId":"c1","attributes":{"a1":"v1","a2":1.5,"a3":false},"published":true},
	"variantsId":"v1","conditions":{"isVisible":true,"isOnlyVariant":false},"unknownField":42}}`

func (r *recorder) ApplyProduct(_ context.Context, e appcatalog.ProductEvent) error {
	r.products = append(r.products, e)

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
	if s.SalesCount != 9007199254740993 || s.AverageRating != 4.7 || s.RatingsCount != 12 {
		t.Fatalf("сигналы: продажи=%d, рейтинг=%v, оценки=%d", s.SalesCount, s.AverageRating, s.RatingsCount)
	}
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

func TestVariantSnapshotCompatibility(t *testing.T) {
	for _, payload := range []string{
		`null`,
		`{"groupId":"v1","axis":{"key":"platform.color","code":"color","name":"Цвет"},"value":"Синий","position":0}`,
	} {
		body := strings.Replace(productBody, `"variantsId":"v1"`, `"variantsId":"v1","variants":`+payload, 1)
		rec, err := handle(t, kafka.TopicProductProjected, message(`["t1","p1"]`, body))
		if err != nil {
			t.Fatal(err)
		}
		card := rec.products[0].Snapshot.Card
		if payload == "null" && (card.VariantsID != "v1" || card.Variants != nil) {
			t.Fatal("null скрывает переключатель, сохраняя группу")
		}
		if payload != "null" && (card.Variants == nil || card.VariantsID != "v1") {
			t.Fatal("потеряна ось")
		}
	}
	body := strings.Replace(
		productBody,
		`"variantsId":"v1"`,
		`"variantsId":"v1","variants":{"groupId":"other",
 "axis":{"key":"platform.color","code":"color","name":"Цвет"},"value":"Синий","position":0}`,
		1,
	)
	if _, err := handle(t, kafka.TopicProductProjected, message(`["t1","p1"]`, body)); err == nil {
		t.Fatal("принята чужая группа")
	}
}

func TestProductRatingNormalization(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields string
		value  float64
		count  int64
	}{
		{"round", `"averageRating":4.333,"ratingsCount":12,`, 4.3, 12},
		{"missing", "", 0, 0},
		{"upper", `"averageRating":7,"ratingsCount":1,`, 5, 1},
		{"lower", `"averageRating":-1,"ratingsCount":1,`, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(productBody, `"averageRating":4.7,"ratingsCount":12,`, tc.fields, 1)
			rec, err := handle(t, kafka.TopicProductProjected, message(`["t1","p1"]`, body))
			if err != nil {
				t.Fatal(err)
			}
			product, err := catalog.NewProduct(rec.products[0].Snapshot, published)
			if err != nil {
				t.Fatal(err)
			}
			state := product.State()
			if state.AverageRating != tc.value || state.RatingsCount != tc.count {
				t.Fatal(state)
			}
			rating := catalog.ProductRating(state.AverageRating, state.RatingsCount)
			if (rating == nil) != (tc.count == 0) {
				t.Fatal(rating)
			}
		})
	}
}

func TestProductAssets(t *testing.T) {
	many := make([]string, 22)
	for i := range many {
		many[i] = fmt.Sprintf("p/%02d.jpg", i)
	}
	manyJSON, _ := json.Marshal(many)
	cases := []struct {
		name   string
		fields string
		want   []string
	}{
		{
			name:   "порядок и очистка",
			fields: `"assets":[" b ","a","b","", "  ","data:x","blob:x",42,null,{},[],true]`,
			want:   []string{"b", "a"},
		},
		{
			name:   "управляющие символы",
			fields: `"assets":["a\u0000b","a\tb","a\u007fb"," ","p/main.jpg"]`,
			want:   []string{"p/main.jpg"},
		},
		{name: "табуляция в image", fields: `"image":"a\tb"`},
		{name: "NUL в image", fields: `"image":"a\u0000b"`},
		{name: "DEL в image", fields: `"image":"a\u007fb"`},
		{name: "лимит", fields: `"assets":` + string(manyJSON), want: many[:20]},
		{
			name:   "байты",
			fields: `"assets":["` + strings.Repeat("я", 257) + `","` + strings.Repeat("я", 256) + `"]`,
			want:   []string{strings.Repeat("я", 256)},
		},
		{name: "image", fields: `"image":"p/main.jpg"`, want: []string{"p/main.jpg"}},
		{name: "пустой массив", fields: `"assets":[],"image":"p/main.jpg"`, want: []string{"p/main.jpg"}},
		{name: "null", fields: `"assets":null,"image":"p/main.jpg"`, want: []string{"p/main.jpg"}},
		{name: "ничего", fields: `"assets":null`},
		{name: "нет поля", fields: `"image":null`},
		{name: "недопустимый image", fields: `"assets":[],"image":"data:x"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(productBody, `"image":null`, tc.fields, 1)
			rec, err := handle(t, kafka.TopicProductProjected, message(`["t1","p1"]`, body))
			if err != nil {
				t.Fatal(err)
			}
			if len(rec.products) != 1 {
				t.Fatalf("сообщение не принято: %d проекций", len(rec.products))
			}
			if got := rec.products[0].Snapshot.Card.Assets; !slices.Equal(got, tc.want) {
				t.Fatalf("галерея: %v, ожидалась %v", got, tc.want)
			}
		})
	}
}
