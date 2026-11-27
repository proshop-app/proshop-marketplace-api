package main

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/searchfeedback"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type searchPurchaseSinkFunc func(context.Context, ports.SearchPurchase) error

func (f searchPurchaseSinkFunc) Purchase(ctx context.Context, purchase ports.SearchPurchase) error {
	return f(ctx, purchase)
}

func TestSearchPurchaseRetriesAndExpires(t *testing.T) {
	ctx := context.Background()
	clock := fake.NewClock(time.Unix(1, 0))
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
	dispatcher := messaging.NewDispatcher(
		store,
		clock,
		messaging.DispatcherConfig{Batch: 4, PerTenant: 1},
		map[string]messaging.Handler{searchfeedback.TaskPurchase: searchfeedback.NewPurchases(sink, clock).Deliver},
	)
	kinds := []string{searchfeedback.TaskPurchase}
	first, _, _, err := dispatcher.RunWork(ctx, kinds)
	if err != nil || len(first) != 1 || errs.CodeOf(first[0].Err) != errs.Unavailable {
		t.Fatal(first, err)
	}
	if outcomes, _, _, err := dispatcher.RunWork(ctx, kinds); len(outcomes) != 0 || err != nil {
		t.Fatal("отложенный повтор либо чужая задача", outcomes, err)
	}
	clock.Advance(time.Second)
	second, _, _, err := dispatcher.RunWork(ctx, kinds)
	if err != nil || len(second) != 1 || second[0].Err != nil || second[0].Task.Attempts != 2 {
		t.Fatal(second, err)
	}
	if !reflect.DeepEqual(got, []ports.SearchPurchase{event, event}) {
		t.Fatal("повтор изменил событие", got)
	}
	enqueuePurchaseTask(t, store, clock.Now(), "t2", "o2")
	clock.Advance(2 * time.Hour)
	outcomes, _, _, err := dispatcher.RunWork(ctx, kinds)
	if len(outcomes) != 1 || outcomes[0].Err != nil || err != nil || len(got) != 2 {
		t.Fatal("просроченная покупка должна завершиться без вызова поиска", outcomes, err, got)
	}
	if !slices.Contains(searchLaneKinds, searchfeedback.TaskPurchase) ||
		!slices.Contains(searchLaneKinds, ports.TaskPurgeTenant) {
		t.Fatal("покупка и очистка магазина обращаются к поиску", searchLaneKinds)
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
