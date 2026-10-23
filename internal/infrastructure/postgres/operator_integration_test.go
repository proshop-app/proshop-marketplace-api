//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
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
	failure := errors.New("временная ошибка")
	replay := &fake.Replay{Err: failure}
	service := messaging.NewEvents(uow, clock.System{}, replay)
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
			_, err := service.Decide(ctx, actor, id, 1, true, "повтор после исправления")
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
	var payload []byte
	payloadQuery := `SELECT payload FROM outbox WHERE kind = 'messaging.event.replay' ORDER BY id DESC LIMIT 1`
	if err := pool.QueryRow(ctx, payloadQuery).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	old := ports.Task{Payload: payload, Attempts: 5}
	if err := service.HandleTask(ctx, old); err != nil {
		t.Fatal(err)
	}
	for _, retry := range []bool{true, false} {
		current, err := service.Decide(ctx, actor, id, 1, retry, "задержавшееся решение")
		if !errors.Is(err, event.ErrConflict) || current.Control.Version != 3 {
			t.Fatal(current, err)
		}
	}
	other := operator.Principal{ID: "op2", Role: operator.Role}
	if _, err := service.Decide(ctx, other, id, 3, true, "новое решение"); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleTask(ctx, old); err != nil || len(replay.Events) != 1 {
		t.Fatal("повтор старой задачи изменил новое решение", err, replay.Events)
	}
	if err := pool.QueryRow(ctx, payloadQuery).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	fresh := ports.Task{Payload: payload, Attempts: 1}
	if err := service.HandleTask(ctx, fresh); !errors.Is(err, failure) {
		t.Fatal("бюджет нового решения", err)
	}
	replay.Err = nil
	fresh.Attempts = 2
	if err := service.HandleTask(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	page, err = service.List(ctx, other, ports.EventFilter{Status: event.Resolved})
	if err != nil || page.Total != 1 || page.Items[0].Control.OperatorID != other.ID ||
		page.Items[0].Control.Version != 5 {
		t.Fatal(page, err)
	}
	if err := pool.QueryRow(ctx, taskQuery).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, decisionQuery, id).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if tasks != 2 || decisions != 4 {
		t.Fatal("устаревшие запросы не создают аудит и задачи", tasks, decisions)
	}
}

func TestISORegionMigrationInvalidatesLegacyDelivery(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var log bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	// Откатываем и последующие миграции, чтобы проверять именно переход 21 → 22.
	rollback := func() {
		t.Helper()
		statuses, err := postgres.MigrationStatus(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range statuses {
			if status.Source.Version < 22 {
				continue
			}
			if err := postgres.MigrateDown(ctx, pool); err != nil {
				t.Fatal(err)
			}
		}
	}
	rollback()
	seed := `INSERT INTO tenants (tenant_id,slug,status,event_at,version,updated_at)
 VALUES ('t1','s1','active',now(),1,now()), ('t2','s2','active',now(),1,now()),
 ('t3','s3','active',now(),1,now());
 INSERT INTO participants (tenant_id,slug,name,status,enabled,shop_enabled,version,
  delivery_settings,created_at,updated_at)
 VALUES ('t1','s1','Магазин','connected',true,true,1,
  '{"regions":["cdek:81"],"carriers":["cdek"]}',now(),now()),
 ('t2','s2','Магазин','onboarding',false,false,7,
  '{"regions":["yandex:1","RU-MOW"],"carriers":["yandex"]}',now(),now()),
 ('t3','s3','Магазин','connected',true,true,4,
  '{"regions":["RU-MOW"],"carriers":["cdek"]}',now(),now());`
	if _, err := pool.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		tenant   string
		archived bool
		version  int64
		regions  []string
		carriers []string
	}{
		{"t1", true, 2, []string{"cdek:81"}, []string{"cdek"}},
		{"t2", true, 8, []string{"yandex:1", "RU-MOW"}, []string{"yandex"}},
		{"t3", false, 4, []string{"RU-MOW"}, []string{"cdek"}},
	} {
		t.Run(tc.tenant, func(t *testing.T) {
			var enabled bool
			var version int64
			var legacy sql.NullString
			var delivery string
			query := `SELECT shop_enabled,version,legacy_delivery_settings::text,delivery_settings::text
 FROM participants WHERE tenant_id = $1`
			if err := pool.QueryRow(ctx, query, tc.tenant).Scan(&enabled, &version, &legacy, &delivery); err != nil {
				t.Fatal(err)
			}
			if enabled == tc.archived || version != tc.version || legacy.Valid != tc.archived {
				t.Fatal(enabled, version, legacy, delivery)
			}
			original, err := json.Marshal(map[string][]string{"regions": tc.regions, "carriers": tc.carriers})
			if err != nil {
				t.Fatal(err)
			}
			if tc.archived {
				if !jsonEqual(t, []byte(legacy.String), original) ||
					!jsonEqual(t, []byte(delivery), []byte(`{"regions":[],"carriers":[]}`)) {
					t.Fatal("архив и очищенное покрытие", legacy, delivery)
				}
			} else if !jsonEqual(t, []byte(delivery), original) {
				t.Fatal("ISO-покрытие сохранено без архива", delivery)
			}
		})
	}
	var reported int
	reportQuery := `SELECT count(*) FROM operator_delivery_migration_report
 WHERE (tenant_id = 't1' AND previous_shop_enabled AND previous_status = 'connected')
 OR (tenant_id = 't2' AND NOT previous_shop_enabled AND previous_status = 'onboarding')`
	if err := pool.QueryRow(ctx, reportQuery).Scan(&reported); err != nil || reported != 2 {
		t.Fatal("отчёт оператора", reported, err)
	}
	if !strings.Contains(log.String(), `"tenantId":"t1"`) ||
		!strings.Contains(log.String(), `"tenantId":"t2"`) || strings.Contains(log.String(), `"tenantId":"t3"`) {
		t.Fatal("журнал миграции", log.String())
	}
	rollback()
	var restored int
	restoredQuery := `SELECT count(*) FROM participants
 WHERE (tenant_id = 't1' AND shop_enabled AND status = 'connected' AND enabled AND version = 3
  AND delivery_settings = '{"regions":["cdek:81"],"carriers":["cdek"]}'::jsonb)
 OR (tenant_id = 't2' AND NOT shop_enabled AND status = 'onboarding' AND NOT enabled AND version = 9
  AND delivery_settings = '{"regions":["yandex:1","RU-MOW"],"carriers":["yandex"]}'::jsonb)
 OR (tenant_id = 't3' AND shop_enabled AND status = 'connected' AND enabled AND version = 4
  AND delivery_settings = '{"regions":["RU-MOW"],"carriers":["cdek"]}'::jsonb)`
	if err := pool.QueryRow(ctx, restoredQuery).Scan(&restored); err != nil || restored != 3 {
		t.Fatal("21 → 22 → 21: доставка, согласие и статус восстановлены", restored, err)
	}
}

func jsonEqual(t *testing.T, actual, expected []byte) bool {
	t.Helper()
	var got, want any
	if err := json.Unmarshal(actual, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(expected, &want); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(got, want)
}
