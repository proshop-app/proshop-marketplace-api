//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

type claimPlanTrace struct {
	sql  string
	args []any
}

func (trace *claimPlanTrace) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, "WITH ordered_heads AS") && !strings.HasPrefix(data.SQL, "EXPLAIN") {
		trace.sql, trace.args = data.SQL, append([]any{}, data.Args...)
	}
	return ctx
}

func (*claimPlanTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestOutboxClaimLocksSelectedIDsByPrimaryKey(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	// Намеренно без ANALYZE: ошибка оценки не должна умножать скан очереди на число кандидатов.
	if _, err := pool.Exec(ctx, bulkFairSeed); err != nil {
		t.Fatal(err)
	}
	config := pool.Config()
	trace := &claimPlanTrace{}
	config.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	tasks, err := postgres.NewTaskQueue(postgres.Single(traced)).Claim(ctx, ports.ClaimRequest{
		Now:        time.Now(),
		Limit:      8,
		PerTenant:  8,
		LeaseUntil: time.Now().Add(time.Minute),
	})
	if err != nil || len(tasks) != 8 || trace.sql == "" {
		t.Fatal(len(tasks), err)
	}
	rows, err := traced.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+trace.sql, trace.args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	bounded := false
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		t.Log(line)
		if strings.Contains(line, "Index Scan using outbox_pkey on outbox o ") &&
			strings.Contains(line, "rows=1 loops=8") {
			bounded = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !bounded {
		t.Fatal("блокировка должна читать только восемь выбранных ID по PK")
	}
}
