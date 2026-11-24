//go:build integration

package postgres_test

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Старый CREATE INDEX ожидает ShareLock за активной записью, блокируя следующих
// писателей. CONCURRENTLY ждёт ту же транзакцию без остановки приёма событий.
func TestLaneIndexBuildDoesNotBlockOutboxWrites(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	migration, err := os.ReadFile("migrations/00070_outbox_lanes.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Split(string(migration), "-- +goose Down")[0]
	pattern := regexp.MustCompile(`(?s)CREATE INDEX[^;]+ON outbox \(kind, tenant_id, next_at, id\)[^;]*;`)
	statement := pattern.FindString(up)
	if statement == "" {
		t.Fatal("не найден индекс готовой очереди")
	}
	if _, err := pool.Exec(ctx, "DROP INDEX IF EXISTS outbox_lane_due_idx"); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	insert := `INSERT INTO outbox (kind, tenant_id, payload, next_at, created_at)
        VALUES ('test', 't1', '{}', now(), now())`
	if _, err := blocker.Exec(ctx, insert); err != nil {
		t.Fatal(err)
	}
	builder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer builder.Release()
	finished := make(chan error, 1)
	go func() {
		_, err := builder.Exec(ctx, statement)
		finished <- err
	}()
	defer func() {
		_ = blocker.Rollback(ctx)
		if err := <-finished; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		waitingQuery := `SELECT EXISTS (
            SELECT 1 FROM pg_locks WHERE pid = $1 AND NOT granted
        )`
		err := pool.QueryRow(ctx, waitingQuery, builder.Conn().PgConn().PID()).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("построение не дождалось активного писателя")
		}
		time.Sleep(time.Millisecond)
	}
	writeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := pool.Exec(writeCtx, insert); err != nil {
		t.Fatal("построение индекса остановило приём", err)
	}
}
