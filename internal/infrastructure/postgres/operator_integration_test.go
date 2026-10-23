//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/event"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func TestOperatorDecisionCASOutboxAndAudit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(pool)
	if err := messaging.NewParker(uow).Park(ctx, ports.ParkedEvent{
		Topic:       "topic",
		TenantID:    "t1",
		ParkedAt:    time.Now(),
		PublishedAt: time.Now(),
		Headers:     map[string]string{"x-tenant": "t1"},
		Value:       []byte(`{"version":1}`),
	}); err != nil {
		t.Fatal(err)
	}
	service := messaging.NewEvents(uow, clock.System{}, nil)
	actor := operator.Principal{ID: "op1", Role: operator.Role}
	page, err := service.List(ctx, actor, ports.EventFilter{TenantID: "t1", Status: event.Parked})
	if err != nil || len(page.Items) != 1 || page.Total != 1 {
		t.Fatal(page, err)
	}
	id := page.Items[0].Control.ID
	var success atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.Decide(ctx, actor, id, true, "повтор после исправления")
			if err == nil {
				success.Add(1)
			} else if !errors.Is(err, event.ErrConflict) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatal("один переход", success.Load())
	}
	page, err = service.List(ctx, actor, ports.EventFilter{Status: event.Queued})
	if err != nil || page.Total != 1 || page.Items[0].Control.OperatorID != actor.ID {
		t.Fatal(page, err)
	}
	var tasks, decisions int
	taskQuery := `SELECT count(*) FROM outbox WHERE kind = 'messaging.event.replay'`
	if err := pool.QueryRow(ctx, taskQuery).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	decisionQuery := `SELECT count(*) FROM parked_event_decisions WHERE event_id = $1`
	if err := pool.QueryRow(ctx, decisionQuery, id).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || decisions != 1 {
		t.Fatal("задача и аудит", tasks, decisions)
	}
}

func TestISORegionMigrationInvalidatesLegacyDelivery(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := postgres.MigrateDown(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO tenants (tenant_id,slug,status,event_at,version,updated_at)
 VALUES ('t1','s1','active',now(),1,now());
 INSERT INTO participants (tenant_id,slug,name,status,enabled,shop_enabled,version,
  delivery_settings,created_at,updated_at)
 VALUES ('t1','s1','Магазин','connected',true,true,1,
  '{"regions":["cdek:81"],"carriers":["cdek"]}',now(),now());`
	if _, err := pool.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	var version int64
	var legacy, delivery string
	query := `SELECT shop_enabled,version,legacy_delivery_settings::text,delivery_settings::text
 FROM participants WHERE tenant_id = 't1'`
	if err := pool.QueryRow(ctx, query).Scan(&enabled, &version, &legacy, &delivery); err != nil {
		t.Fatal(err)
	}
	if enabled || version != 2 || legacy == delivery {
		t.Fatal(enabled, version, legacy, delivery)
	}
}
