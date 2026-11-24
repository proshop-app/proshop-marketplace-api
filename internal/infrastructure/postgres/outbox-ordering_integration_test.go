//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const orderedSeed = `INSERT INTO outbox (kind, tenant_id, payload, next_at, created_at)
    SELECT 'ordered', 'A', '{}', now(), now() FROM generate_series(1, 3)`

func TestOrderedTaskCannotOvertakeLeaseOrRetry(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, orderedSeed); err != nil {
		t.Fatal(err)
	}
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	request := ports.ClaimRequest{
		Now:          time.Now(),
		Limit:        8,
		PerTenant:    8,
		OrderedKinds: []string{"ordered"},
		LeaseUntil:   time.Now().Add(time.Minute),
	}
	first, err := queue.Claim(ctx, request)
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	// Второй процесс не может обогнать арендованное намерение того же вида и магазина.
	next, err := postgres.NewTaskQueue(postgres.Single(pool)).Claim(ctx, request)
	if err != nil || len(next) != 0 {
		t.Fatal("обгон аренды", next, err)
	}
	if err := queue.Retry(ctx, first[0].ID, first[0].Attempts, time.Now().Add(time.Hour), "retry"); err != nil {
		t.Fatal(err)
	}
	next, err = queue.Claim(ctx, request)
	if err != nil || len(next) != 0 {
		t.Fatal("обгон отложенного повтора", next, err)
	}
	if err := queue.Complete(ctx, first[0].ID, first[0].Attempts, time.Now()); err != nil {
		t.Fatal(err)
	}
	next, err = queue.Claim(ctx, request)
	if err != nil || len(next) != 1 || next[0].ID <= first[0].ID {
		t.Fatal(next, err)
	}
}
