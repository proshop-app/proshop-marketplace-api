package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/searchfeedback"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type searchPurchaseSinkFunc func(context.Context, ports.SearchPurchase) error

func (f searchPurchaseSinkFunc) Purchase(ctx context.Context, purchase ports.SearchPurchase) error {
	return f(ctx, purchase)
}

func TestSearchPurchaseDispatcherRetriesWithSameEvent(t *testing.T) {
	ctx := context.Background()
	clock := fake.NewClock(time.Now().UTC())
	store := fake.NewStore()
	event := enqueuePurchaseTask(t, store, clock.Now(), "t1", "o1")
	if err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().Enqueue(ctx, ports.NewTask{Kind: "other", TenantID: "t1", At: clock.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	var got []ports.SearchPurchase
	sink := searchPurchaseSinkFunc(func(_ context.Context, purchase ports.SearchPurchase) error {
		got = append(got, purchase)
		if len(got) == 1 {
			return errs.New(errs.Unavailable, "search.unavailable", "поиск ответил HTTP 503")
		}
		return nil
	})
	dispatcher := searchPurchaseDispatcher(store, clock, sink)
	first, err := dispatcher.RunOnce(ctx)
	if err != nil || len(first) != 1 || errs.CodeOf(first[0].Err) != errs.Unavailable {
		t.Fatal(first, err)
	}
	if outcomes, err := dispatcher.RunOnce(ctx); len(outcomes) != 0 || err != nil {
		t.Fatal("отложенный повтор либо чужая задача", outcomes, err)
	}
	clock.Advance(time.Second)
	second, err := dispatcher.RunOnce(ctx)
	if err != nil || len(second) != 1 || second[0].Err != nil || second[0].Task.Attempts != 2 {
		t.Fatal(second, err)
	}
	if !reflect.DeepEqual(got, []ports.SearchPurchase{event, event}) {
		t.Fatal("повтор изменил событие", got)
	}
	if outcomes, err := dispatcher.RunOnce(ctx); len(outcomes) != 0 || err != nil {
		t.Fatal(outcomes, err)
	}
}

func TestSearchPurchaseDispatcherBoundsLaneAndExpires(t *testing.T) {
	ctx := context.Background()
	clock := fake.NewClock(time.Now().UTC())
	store := fake.NewStore()
	for index, tenant := range []ids.TenantID{"t1", "t1", "t2", "t3", "t4", "t5"} {
		enqueuePurchaseTask(t, store, clock.Now(), tenant, string(rune('a'+index)))
	}
	terminal := errs.New(errs.Invalid, "search.purchase_rejected", "поиск отклонил покупку")
	sent := make(chan string, 6)
	sink := searchPurchaseSinkFunc(func(_ context.Context, purchase ports.SearchPurchase) error {
		sent <- purchase.OfferIDs[0]
		return terminal
	})
	dispatcher := searchPurchaseDispatcher(store, clock, sink)
	outcomes, err := dispatcher.RunOnce(ctx)
	if err != nil || len(outcomes) != 4 {
		t.Fatal(outcomes, err)
	}
	tenants := make(map[ids.TenantID]bool)
	for _, outcome := range outcomes {
		if tenants[outcome.Task.TenantID] || outcome.FailureReason != terminal.Key() {
			t.Fatal("превышена квота магазина или терминальный отказ не сохранён", outcomes)
		}
		tenants[outcome.Task.TenantID] = true
	}
	clock.Advance(2 * time.Hour)
	outcomes, err = dispatcher.RunOnce(ctx)
	if err != nil || len(outcomes) != 2 || len(sent) != 4 {
		t.Fatal("просроченные и терминальные события не отправляются", outcomes, err, len(sent))
	}
}

func enqueuePurchaseTask(
	t *testing.T,
	store *fake.Store,
	paidAt time.Time,
	tenantID ids.TenantID,
	orderID string,
) ports.SearchPurchase {
	t.Helper()
	event := ports.SearchPurchase{
		Version:        1,
		EventID:        "marketplace-paid:checkout-17:" + string(tenantID) + ":" + orderID,
		BuyerKey:       "e414f05f65a0bfe8e784db84e0678c60035d13dfa7b0e65f4a1f31f250cbb5b2",
		OrderCreatedAt: paidAt.Add(-time.Minute),
		PaidObservedAt: paidAt,
		OfferIDs:       []string{string(tenantID) + "~p1"},
	}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().Enqueue(ctx, ports.NewTask{
			Kind:     searchfeedback.TaskPurchase,
			TenantID: tenantID,
			DedupKey: event.EventID,
			Payload:  payload,
			At:       paidAt,
		})
	}); err != nil {
		t.Fatal(err)
	}
	return event
}
