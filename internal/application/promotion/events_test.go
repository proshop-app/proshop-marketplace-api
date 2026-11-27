package promotion_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	app "github.com/proshop-app/proshop-marketplace/internal/application/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
)

type eventStore struct {
	*fake.Store
	transactions int
	calls        int
}

type eventTx struct {
	ports.Tx
	store *eventStore
}

type eventRepo struct {
	promotion.Repository
	store *eventStore
}

type eventMetrics struct {
	events []promotion.CountedBeacon
}

func TestBeaconBatchCountsFreshEventsOnlyAfterCommit(t *testing.T) {
	ctx := context.Background()
	e := setup()
	active := e.create(t, "category_slot")
	draft := e.create(t, "search_slot")
	active.Status = promotion.Active
	e.store.SeedPromotion(active)
	e.clock.Advance(72 * time.Hour)
	store, metrics := &eventStore{Store: e.store}, &eventMetrics{}
	service := app.NewService(store, e.clock, &fake.IDs{}, e.index, app.Config{
		Location: time.UTC,
		Random:   fake.Random{},
		Metrics:  metrics,
	})
	first := app.Event{
		EventID:     "10000000-0000-4000-8000-000000000001",
		PromotionID: active.ID,
		Kind:        "view",
	}
	events := []app.Event{
		first,
		first,
		{
			EventID:     "10000000-0000-4000-8000-000000000002",
			PromotionID: active.ID,
			Kind:        "view",
		},
		{
			EventID:     "10000000-0000-4000-8000-000000000003",
			PromotionID: active.ID,
			Kind:        "click",
		},
		{
			EventID:     "10000000-0000-4000-8000-000000000004",
			PromotionID: draft.ID,
			Kind:        "click",
		},
		{
			EventID:     "10000000-0000-4000-8000-000000000005",
			PromotionID: "не uuid",
			Kind:        "view",
		},
	}
	if err := service.Events(ctx, events[5:]); err != nil || store.transactions != 0 {
		t.Fatalf("пачка без валидных UUID: %v, транзакций %d", err, store.transactions)
	}
	rollback := errors.New("откат фиксации")
	store.FailCommit = rollback
	if err := service.Events(ctx, events); !errors.Is(err, rollback) || len(metrics.events) != 0 {
		t.Fatalf("метрики после отката: %v, %v", metrics.events, err)
	}
	store.FailCommit = nil
	for range 2 {
		if err := service.Events(ctx, events); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(24 * time.Hour)
	}
	if store.transactions != 3 || store.calls != 3 || len(metrics.events) != 3 {
		t.Fatalf("транзакций %d, пачек %d, метрики %v", store.transactions, store.calls, metrics.events)
	}
	if metrics.events[0].TenantID != active.TenantID || metrics.events[0].PlacementCode != active.PlacementCode {
		t.Fatal(metrics.events)
	}
	rows, err := e.service.Stats(ctx, app.Access{}, active.ID, "2026-11-09", "2026-11-15")
	if err != nil || len(rows) != 1 || rows[0].Impressions != 2 || rows[0].Clicks != 1 {
		t.Fatal(rows, err)
	}
}

func (s *eventStore) Do(ctx context.Context, run func(context.Context, ports.Tx) error) error {
	s.transactions++
	return s.Store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return run(ctx, eventTx{Tx: tx, store: s})
	})
}

func (tx eventTx) Promotion() promotion.Repository {
	return eventRepo{Repository: tx.Tx.Promotion(), store: tx.store}
}

func (r eventRepo) CountEvents(
	ctx context.Context,
	day string,
	events []promotion.Beacon,
) ([]promotion.CountedBeacon, error) {
	r.store.calls++
	return r.Repository.CountEvents(ctx, day, events)
}

func (m *eventMetrics) PromotionEvent(event promotion.CountedBeacon) {
	m.events = append(m.events, event)
}

func (m *eventMetrics) PromotionTransition(promotion.CampaignState, promotion.Status) {}
