//go:build integration

package postgres_test

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
)

const throughputSeed = `INSERT INTO outbox (kind, tenant_id, payload, next_at, created_at)
    SELECT 'point', 'A', '{}', now(), now() FROM generate_series(1, 10000)`

type countingClaimQueue struct {
	ports.TaskQueue
	claims int
}

func (q *countingClaimQueue) Claim(ctx context.Context, request ports.ClaimRequest) (ports.ClaimResult, error) {
	q.claims++
	return q.TaskQueue.Claim(ctx, request)
}

// TestDispatcherPostgresThroughput — явный замер с настоящим Claim и Complete, без запуска стенда.
func TestDispatcherPostgresThroughput(t *testing.T) {
	if os.Getenv("OUTBOX_BENCHMARK") != "1" {
		t.Skip("OUTBOX_BENCHMARK=1: 10000 задач с обработчиком 5 мс")
	}
	pool := pgtest.Pool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, throughputSeed); err != nil {
		t.Fatal(err)
	}
	q := &countingClaimQueue{TaskQueue: postgres.NewTaskQueue(postgres.Single(pool))}
	var handled atomic.Int64
	d := messaging.NewDispatcher(
		q,
		clock.System{},
		messaging.DispatcherConfig{Batch: 200, PerTenant: 200},
		map[string]messaging.Handler{
			"point": func(context.Context, ports.Task) error {
				time.Sleep(5 * time.Millisecond)
				handled.Add(1)
				return nil
			},
		},
	)
	// Совпадает с регистрацией в сервере: пустой пакетный вид тоже участвует в Claim.
	d.RegisterBatch("unused", func(context.Context, []ports.Task) []error { return nil })
	started := time.Now()
	for handled.Load() < 10000 {
		outcomes, err := d.RunOnce(ctx)
		if err != nil || len(outcomes) == 0 {
			t.Fatal(len(outcomes), err)
		}
	}
	elapsed := time.Since(started)
	t.Logf(
		"tasks=10000 handler=5ms elapsed=%s tasks/s=%.2f claims=%d claims/1000=%.1f",
		elapsed,
		10000/elapsed.Seconds(),
		q.claims,
		float64(q.claims)/10,
	)
}

// TestDispatcherPostgresFairness измеряет одну и ту же очередь на разных ревизиях.
func TestDispatcherPostgresFairness(t *testing.T) {
	if os.Getenv("OUTBOX_BENCHMARK") != "1" {
		t.Skip("OUTBOX_BENCHMARK=1: 50000 задач A и одна новая B")
	}
	pool := pgtest.Pool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO outbox (kind, tenant_id, payload, next_at, created_at)
        SELECT 'point', 'A', '{}', now(), now() FROM generate_series(1, 50000)`); err != nil {
		t.Fatal(err)
	}
	// Сравниваем установившийся воркер: создание соединений не входит в ожидание новой задачи.
	connections := make([]*pgxpool.Conn, 0, 8)
	for range 8 {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
	}
	for _, conn := range connections {
		conn.Release()
	}
	var calls atomic.Int64
	var waiting time.Time
	var quietDelay time.Duration
	d := messaging.NewDispatcher(
		postgres.NewTaskQueue(postgres.Single(pool)),
		clock.System{},
		messaging.DispatcherConfig{Batch: 200, PerTenant: 200},
		map[string]messaging.Handler{
			"point": func(ctx context.Context, task ports.Task) error {
				if task.TenantID == "B" {
					quietDelay = time.Since(waiting)
					return nil
				}
				if calls.Add(1) == 1 {
					waiting = time.Now()
					if _, err := pool.Exec(ctx, `INSERT INTO outbox
                        (kind, tenant_id, payload, next_at, created_at)
                        VALUES ('point', 'B', '{}', now(), now())`); err != nil {
						return err
					}
				}
				time.Sleep(5 * time.Millisecond)
				return nil
			},
		},
	)
	d.RegisterBatch("unused", func(context.Context, []ports.Task) []error { return nil })
	started := time.Now()
	if _, err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	firstCalls, pass := calls.Load(), time.Since(started)
	if _, err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("backlog=50000 first-pass-A=%d pass=%s B-wait=%s", firstCalls, pass, quietDelay)
	if quietDelay == 0 {
		t.Fatal("B не получил слот во втором проходе")
	}
}
