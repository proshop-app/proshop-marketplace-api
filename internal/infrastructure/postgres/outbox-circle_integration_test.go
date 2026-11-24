//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	circleKind = "participant.settings.sync"
	// seedBlockedCircle: у a1..a8 старейшая задача упорядоченного вида в повторе, следующая созрела;
	// у b01..b50 одна созревшая задача.
	seedBlockedCircle = `INSERT INTO outbox (kind, tenant_id, payload, next_at, created_at, attempts)
        SELECT $1, 'a' || n, '{}'::jsonb, now() + interval '10 minutes', now(), 1 FROM generate_series(1, 8) n
        UNION ALL
        SELECT $1, 'a' || n, '{}'::jsonb, now() - interval '1 minute', now(), 0 FROM generate_series(1, 8) n
        UNION ALL
        SELECT $1, 'b' || lpad(n::text, 2, '0'), '{}'::jsonb, now() - interval '1 minute', now(), 0
        FROM generate_series(1, 50) n
        ORDER BY 2, 4 DESC`
	// seedDeferredCircle: 300 магазинов только с отложенными задачами, за ними 5 с созревшими.
	seedDeferredCircle = `INSERT INTO outbox (kind, tenant_id, payload, next_at, created_at)
        SELECT 'circle', 'd' || lpad(n::text, 3, '0'), '{}'::jsonb, now() + interval '10 minutes', now()
        FROM generate_series(1, 300) n
        UNION ALL
        SELECT 'circle', 'r' || n, '{}'::jsonb, now() - interval '1 minute', now() FROM generate_series(1, 5) n`
)

// Сценарий ревью: полосы со старейшей задачей в повторе не считаются готовыми, курсор двигается
// и при пустом взятии — остальные магазины обслуживаются.
func TestClaimCircleSkipsBlockedOrderedLanes(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, seedBlockedCircle, circleKind); err != nil {
		t.Fatal(err)
	}
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	served := map[ids.TenantID]bool{}
	after := ids.TenantID("a0")
	for range 10 {
		claimed, err := queue.Claim(ctx, ports.ClaimRequest{
			Now:          time.Now(),
			Limit:        8,
			PerTenant:    8,
			AfterTenant:  after,
			OrderedKinds: []string{circleKind},
			LeaseUntil:   time.Now().Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range claimed.Tasks {
			if task.TenantID[0] == 'a' {
				t.Fatal("взята задача за головой в повторе", task)
			}
			served[task.TenantID] = true
		}
		after = claimed.Next
	}
	if len(served) != 50 {
		t.Fatalf("круг обслужил %d магазинов из 50", len(served))
	}
}

// Взятие посещает не больше ClaimLaneVisits полос вида и двигает курсор и при пустом обходе;
// старейшая созревшая задача вида берётся сразу, даже если круг до её магазина не дошёл.
func TestClaimVisitsBoundedLanes(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, seedDeferredCircle); err != nil {
		t.Fatal(err)
	}
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	var after ids.TenantID
	taken := map[ids.TenantID]bool{}
	for pass := range 4 {
		claimed, err := queue.Claim(ctx, ports.ClaimRequest{
			Now:         time.Now(),
			Limit:       8,
			PerTenant:   8,
			Kinds:       []string{"circle"},
			AfterTenant: after,
			LeaseUntil:  time.Now().Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		if pass == 0 && (claimed.Next != "d128" || len(claimed.Tasks) != 1 || claimed.Tasks[0].TenantID != "r1") {
			t.Fatalf("первое взятие: старейшая задача и %d посещённых полос: %+v", ports.ClaimLaneVisits, claimed)
		}
		for _, task := range claimed.Tasks {
			taken[task.TenantID] = true
		}
		after = claimed.Next
	}
	if len(taken) != 5 {
		t.Fatalf("созревшие задачи за отложенными не взяты: %v", taken)
	}
}
