//go:build integration

package postgres_test

import (
	"context"
	"regexp"
	"slices"
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
	// seedSharedTaskCircle: общая задача площадки (пустой магазин) в повторе; у m001..m300 голова
	// упорядоченного вида в повторе, а следующая задача созрела раньше всех — подсказка старейшей
	// не помогает; у z1..z5 одна созревшая задача.
	seedSharedTaskCircle = `INSERT INTO outbox (kind, tenant_id, payload, next_at, created_at, attempts)
        SELECT 'reference.changed', '', '{}'::jsonb, now() + interval '10 minutes', now(), 1
        UNION ALL
        SELECT $1, 'm' || lpad(n::text, 3, '0'), '{}'::jsonb, now() + interval '10 minutes', now(), 1
        FROM generate_series(1, 300) n
        UNION ALL
        SELECT $1, 'm' || lpad(n::text, 3, '0'), '{}'::jsonb, now() - interval '2 minutes', now(), 0
        FROM generate_series(1, 300) n
        UNION ALL
        SELECT $1, 'z' || n, '{}'::jsonb, now() - interval '1 minute', now(), 0 FROM generate_series(1, 5) n
        ORDER BY 2, 4 DESC`
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
	after := ports.ClaimAfter("a0")
	for range 10 {
		claimed, err := queue.Claim(ctx, ports.ClaimRequest{
			Now:          time.Now(),
			Limit:        8,
			PerTenant:    8,
			After:        after,
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
	var after ports.ClaimCursor
	taken := map[ids.TenantID]bool{}
	for pass := range 4 {
		claimed, err := queue.Claim(ctx, ports.ClaimRequest{
			Now:        time.Now(),
			Limit:      8,
			PerTenant:  8,
			Kinds:      []string{"circle"},
			After:      after,
			LeaseUntil: time.Now().Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		first := claimed.Next == ports.ClaimAfter("d128") && len(claimed.Tasks) == 1 &&
			claimed.Tasks[0].TenantID == "r1"
		if pass == 0 && !first {
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

// Общая задача площадки с пустым магазином, оставшаяся в очереди (повтор, аренда), не возвращает
// круг в начало: курсор на пустом магазине — начатый круг, а не «круг не начат». Иначе взятие
// без видов запроса не уходит дальше первых ClaimLaneVisits полос, и z1..z5 не обслуживаются.
func TestClaimCirclePassesSharedTaskWithEmptyTenant(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, seedSharedTaskCircle, circleKind); err != nil {
		t.Fatal(err)
	}
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	served := map[ids.TenantID]bool{}
	var after ports.ClaimCursor
	for range 12 {
		claimed, err := queue.Claim(ctx, ports.ClaimRequest{
			Now:          time.Now(),
			Limit:        8,
			PerTenant:    8,
			After:        after,
			OrderedKinds: postgres.OrderedTaskKinds,
			LeaseUntil:   time.Now().Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range claimed.Tasks {
			if task.TenantID == "" || task.TenantID[0] == 'm' {
				t.Fatal("взята задача в повторе или за головой в повторе", task)
			}
			served[task.TenantID] = true
		}
		if !claimed.Next.Started {
			t.Fatalf("курсор после взятия не начат: %+v", claimed)
		}
		after = claimed.Next
	}
	if len(served) != 5 {
		t.Fatalf("круг обслужил %d магазинов из 5: %v", len(served), served)
	}
}

// Частичный индекс головы полосы (миграция 00080) построен ровно по упорядоченным видам: вид вне
// предиката читал бы полосу без индекса, лишний вид в предикате — мёртвый вес индекса.
func TestOrderedHeadIndexCoversOrderedKinds(t *testing.T) {
	pool := testPool(t)
	var definition string
	err := pool.QueryRow(context.Background(), `SELECT pg_get_indexdef('outbox_ordered_head_idx'::regclass)`).
		Scan(&definition)
	if err != nil {
		t.Fatal(err)
	}
	indexed := []string{}
	for _, match := range regexp.MustCompile(`'([^']+)'::text`).FindAllStringSubmatch(definition, -1) {
		indexed = append(indexed, match[1])
	}
	slices.Sort(indexed)
	if !slices.Equal(indexed, slices.Sorted(slices.Values(postgres.OrderedTaskKinds))) {
		t.Fatal(definition)
	}
}

// Взятие вида, которого нет в очереди, возвращает курсор как был; курсор на пустом магазине при
// обычных магазинах — начатый круг: первое взятие берёт их, а не начинает заново.
func TestClaimKeepsCursorWhenKindIsAbsent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, seedBlockedCircle, circleKind); err != nil {
		t.Fatal(err)
	}
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	request := ports.ClaimRequest{Now: time.Now(), Limit: 8, PerTenant: 8, LeaseUntil: time.Now().Add(time.Hour)}
	for _, after := range []ports.ClaimCursor{{}, ports.ClaimAfter("x"), ports.ClaimAfter("")} {
		request.After, request.Kinds = after, []string{"checkout.create"}
		claimed, err := queue.Claim(ctx, request)
		if err != nil || len(claimed.Tasks) != 0 || claimed.Next != after {
			t.Fatalf("курсор %+v: %+v %v", after, claimed, err)
		}
	}
	request.After, request.Kinds = ports.ClaimAfter(""), nil
	request.OrderedKinds = []string{circleKind}
	claimed, err := queue.Claim(ctx, request)
	if err != nil || len(claimed.Tasks) != 8 || claimed.Next.Tenant == "" {
		t.Fatalf("курсор на пустом магазине: %+v %v", claimed, err)
	}
}
