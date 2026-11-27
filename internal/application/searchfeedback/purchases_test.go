package searchfeedback

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

type purchaseSink struct {
	got       []ports.SearchPurchase
	deadlines []time.Duration
	err       error
}

func (s *purchaseSink) Purchase(ctx context.Context, purchase ports.SearchPurchase) error {
	s.got = append(s.got, purchase)
	deadline, _ := ctx.Deadline()
	s.deadlines = append(s.deadlines, time.Until(deadline))
	return s.err
}

func TestPurchaseDeliverySendsSameEventWithinOwnBudget(t *testing.T) {
	clock := fake.NewClock(time.Now().UTC())
	event := purchaseEvent(clock.Now())
	task := purchaseTask(t, event)
	sink := &purchaseSink{err: errs.New(errs.Unavailable, "search.unavailable", "поиск ответил HTTP 503")}
	purchases := NewPurchases(sink, clock)

	// у общей полосы срок обработчика больше: попытка покупки ограничена своим бюджетом
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := purchases.Deliver(ctx, task); errs.CodeOf(err) != errs.Unavailable {
		t.Fatal("отказ поиска должен вернуться на повтор", err)
	}
	sink.err = nil
	if err := purchases.Deliver(ctx, task); err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != 2 || sink.got[0].EventID != event.EventID || sink.got[1].EventID != event.EventID {
		t.Fatal("повтор изменил событие", sink.got)
	}
	for _, left := range sink.deadlines {
		if left <= 0 || left > PurchaseCallTimeout {
			t.Fatal("попытка покупки не ограничена своим сроком", left)
		}
	}
}

func TestPurchaseDeliveryDropsExpiredAndRejectsBroken(t *testing.T) {
	clock := fake.NewClock(time.Now().UTC())
	sink := &purchaseSink{}
	purchases := NewPurchases(sink, clock)
	task := purchaseTask(t, purchaseEvent(clock.Now()))

	clock.Advance(purchaseLifetime)
	if err := purchases.Deliver(context.Background(), task); err != nil || len(sink.got) != 0 {
		t.Fatal("просроченная покупка завершается без вызова поиска", err, sink.got)
	}
	broken := ports.Task{Kind: TaskPurchase, Payload: []byte(`{"version":1}`)}
	if err := purchases.Deliver(context.Background(), broken); errs.CodeOf(err) != errs.Invalid {
		t.Fatal("неполное событие — ошибка формы", err)
	}
}

func purchaseEvent(paidAt time.Time) ports.SearchPurchase {
	return ports.SearchPurchase{
		Version:        PurchaseVersion,
		EventID:        "marketplace-paid:checkout-17:t1:o1",
		BuyerKey:       "e414f05f65a0bfe8e784db84e0678c60035d13dfa7b0e65f4a1f31f250cbb5b2",
		OrderCreatedAt: paidAt.Add(-time.Minute),
		PaidObservedAt: paidAt,
		OfferIDs:       []string{"t1~p1"},
	}
}

func purchaseTask(t *testing.T, event ports.SearchPurchase) ports.Task {
	t.Helper()
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return ports.Task{Kind: TaskPurchase, TenantID: "t1", Payload: payload}
}
