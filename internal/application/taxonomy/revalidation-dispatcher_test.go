package taxonomy_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestRevalidationDispatcherYieldsBetweenBatches(t *testing.T) {
	store := fake.NewStore()
	seedValues(t, store, "v1")
	clock := fake.NewClock(time.Now())
	service := apptaxonomy.NewService(store, clock, nil, 0)
	for i := 0; i < 405; i++ {
		store.SeedProduct(catalog.ProductState{
			TenantID:  "t1",
			ProductID: ids.ProductID(fmt.Sprintf("p%03d", i)),
			Version:   1,
		})
	}
	ctx := context.Background()
	if err := applyDefinition(ctx, service, schema(1)); err != nil {
		t.Fatal(err)
	}
	if err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().Enqueue(ctx, ports.NewTask{Kind: "neighbor", At: clock.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	neighbor := false
	handlers := map[string]messaging.Handler{
		apptaxonomy.TaskRevalidate: service.HandleTask,
		"neighbor": func(context.Context, ports.Task) error {
			neighbor = true
			if store.ProductPageCalls() != 1 || readProgress(t, store).Done {
				t.Fatal("соседняя задача ждала весь проход")
			}
			return nil
		},
	}
	for _, kind := range []string{"catalog.offer.sync", "moderation.offer.status", ports.TaskStoreCard} {
		handlers[kind] = func(context.Context, ports.Task) error { return nil }
	}
	dispatcher := messaging.NewDispatcher(
		store,
		clock,
		messaging.DispatcherConfig{Batch: 1000, PerTenant: 1000, Lease: time.Minute},
		handlers,
	)
	for batch := 1; batch <= 3; batch++ {
		outcomes, err := dispatcher.RunOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, outcome := range outcomes {
			if outcome.Err != nil {
				t.Fatal(outcome.Err)
			}
		}
		if store.ProductPageCalls() != batch {
			t.Fatal("вызов обработал больше одной пачки")
		}
	}
	if !neighbor || !readProgress(t, store).Done {
		t.Fatal("цепочка не завершена")
	}
}
