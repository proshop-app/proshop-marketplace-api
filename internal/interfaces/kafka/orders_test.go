package kafka_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

const (
	orderCheckoutID = "6f1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b"
	orderKey        = `["t1","o1"]`
	orderBody       = `{"version":1,"tenantId":"t1","externalCheckoutId":"` + orderCheckoutID + `:t1",
	"orderId":"o1","status":"awaiting_pickup","revision":9,
	"tracking":{"carrier":"carrier","number":"TRACK1","url":"https://example.test/TRACK1"},"unknownField":1}`
)

type orderRecorder struct {
	events []appcheckout.OrderEvent
	err    error
}

func (r *orderRecorder) ApplyOrderEvent(_ context.Context, event appcheckout.OrderEvent) error {
	r.events = append(r.events, event)
	return r.err
}

func handleOrder(key, body string) (*orderRecorder, error) {
	rec := &orderRecorder{}
	router := kafka.NewRouter(&recorder{}, &recorder{}).WithOrders(rec)
	envelope := kafka.Envelope{Topic: kafka.TopicOrderStatusChanged, EventID: "ev", Message: message(key, body)}
	return rec, router.Handle(context.Background(), envelope)
}

func TestOrderStatusChanged(t *testing.T) {
	rec, err := handleOrder(orderKey, orderBody)
	if err != nil || len(rec.events) != 1 {
		t.Fatalf("событие статуса: %v %+v", err, rec.events)
	}
	event := rec.events[0]
	if event.EventID != "ev" || event.TenantID != "t1" || string(event.CheckoutID) != orderCheckoutID {
		t.Fatalf("адрес покупки: %+v", event)
	}
	order := event.Order
	if order.OrderID != "o1" || order.Status != checkout.GroupAwaitingPickup || order.Revision != 9 {
		t.Fatalf("состояние заказа: %+v", order)
	}
	if order.Tracking == nil || order.Tracking.Carrier != "carrier" || *order.Tracking.Number != "TRACK1" {
		t.Fatalf("отслеживание: %+v", order.Tracking)
	}
}

func TestOrderStatusChangedDropsUnsafeTracking(t *testing.T) {
	body := strings.Replace(orderBody, "https://example.test/TRACK1", "javascript:alert(1)", 1)
	rec, err := handleOrder(orderKey, body)
	if err != nil || rec.events[0].Order.Tracking != nil {
		t.Fatalf("небезопасная ссылка не доходит до покупателя: %v %+v", err, rec.events)
	}
}

func TestOrderStatusChangedRejects(t *testing.T) {
	cases := map[string][2]string{
		"ключ не пара":            {`"o1"`, orderBody},
		"чужой магазин в ключе":   {`["t2","o1"]`, orderBody},
		"другой заказ в ключе":    {`["t1","o2"]`, orderBody},
		"неизвестная версия":      {orderKey, strings.Replace(orderBody, `"version":1`, `"version":2`, 1)},
		"локальный статус группы": {orderKey, strings.Replace(orderBody, "awaiting_pickup", "seller_unreachable", 1)},
		"нет номера перехода":     {orderKey, strings.Replace(orderBody, `"revision":9`, `"revision":0`, 1)},
		"ключ чужого оформления":  {orderKey, strings.Replace(orderBody, ":t1\",", ":t2\",", 1)},
		"ключ оформления не UUID": {orderKey, strings.Replace(orderBody, orderCheckoutID, "покупка", 1)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec, err := handleOrder(tc[0], tc[1])
			rejected := errors.Is(err, kafka.ErrInvalidPayload) || errors.Is(err, kafka.ErrTenantMismatch)
			if !rejected || len(rec.events) != 0 {
				t.Fatalf("сообщение должно уйти в отравленные: %v %+v", err, rec.events)
			}
		})
	}
}

func TestOrderStatusTopicHasOwnLane(t *testing.T) {
	router := kafka.NewRouter(&recorder{}, &recorder{}).WithOrders(&orderRecorder{})
	regular, bulk := kafka.SubscriptionTopics("cell", router)
	topic := kafka.OrderStatusTopic("cell")
	if topic != "cell."+kafka.TopicOrderStatusChanged || bulk == topic {
		t.Fatalf("топик статусов: %q", topic)
	}
	for _, name := range regular {
		if name == topic {
			t.Fatal("статусы заказов не должны ждать в общей полосе проекций")
		}
	}
}
