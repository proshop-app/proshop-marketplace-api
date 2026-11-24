//go:build integration

package postgres_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// outboxScaleRuns — повторы взятия для медианы; каждый повтор берёт задачи в аренду по-настоящему.
// outboxChurnPasses — проходы «взять и завершить» для замера оборота.
const (
	outboxScaleRuns   = 15
	outboxChurnPasses = 100
)

type outboxScaleShape struct {
	name    string
	request func(now time.Time) ports.ClaimRequest
}

type statementTrace struct {
	mu   sync.Mutex
	sqls []string
	args [][]any
}

// Виды и квоты повторяют сборку cmd/marketplace/server.go, search-feedback.go и checkout/executor.go.
var (
	scaleCheckoutKinds = []string{
		"checkout.create_order",
		"checkout.expire_creation",
		"checkout.poll_order",
		"checkout.cancel_order",
		"search.purchase",
	}
	scaleBatchKinds   = []string{"catalog.offer.sync", "moderation.offer.status"}
	scaleOrderedKinds = []string{
		"participant.settings.sync",
		"moderation.participant.status",
		"reference.changed",
		"search-synonyms.changed",
	}
	// tenantScaleShapes — взятия засева scripts/bench/outbox-seed-tenants.sql (10 тысяч магазинов).
	tenantScaleShapes = []outboxScaleShape{
		{name: "single-with-cursor", request: func(now time.Time) ports.ClaimRequest {
			return ports.ClaimRequest{
				Now:          now,
				ExcludeKinds: append(slices.Clone(scaleCheckoutKinds), scaleBatchKinds...),
				OrderedKinds: scaleOrderedKinds,
				AfterTenant:  "m02500",
				Limit:        6,
				PerTenant:    8,
				LeaseUntil:   now.Add(90 * time.Second),
			}
		}},
		{name: "one-kind-from-start", request: func(now time.Time) ports.ClaimRequest {
			return ports.ClaimRequest{
				Now:        now,
				Kinds:      []string{"catalog.tenant.sync"},
				Limit:      16,
				PerTenant:  2,
				LeaseUntil: now.Add(10 * time.Second),
			}
		}},
	}
	outboxScaleShapes = []outboxScaleShape{
		{name: "dispatcher-batch", request: func(now time.Time) ports.ClaimRequest {
			return ports.ClaimRequest{
				Now:          now,
				Kinds:        scaleBatchKinds,
				ExcludeKinds: scaleCheckoutKinds,
				AfterTenant:  "b0003",
				Limit:        200,
				PerTenant:    200,
				LeaseUntil:   now.Add(90 * time.Second),
			}
		}},
		{name: "dispatcher-single", request: func(now time.Time) ports.ClaimRequest {
			return ports.ClaimRequest{
				Now:          now,
				ExcludeKinds: append(slices.Clone(scaleCheckoutKinds), scaleBatchKinds...),
				OrderedKinds: scaleOrderedKinds,
				AfterTenant:  "b0500",
				Limit:        6,
				PerTenant:    8,
				LeaseUntil:   now.Add(90 * time.Second),
			}
		}},
		{name: "checkout-create", request: func(now time.Time) ports.ClaimRequest {
			return ports.ClaimRequest{
				Now:         now,
				Kinds:       []string{"checkout.create_order"},
				TenantSlots: map[ids.TenantID]int{},
				Limit:       16,
				PerTenant:   2,
				LeaseUntil:  now.Add(10 * time.Second),
			}
		}},
		{name: "search-purchase", request: func(now time.Time) ports.ClaimRequest {
			return ports.ClaimRequest{
				Now:        now,
				Kinds:      []string{"search.purchase"},
				Limit:      4,
				PerTenant:  1,
				LeaseUntil: now.Add(10 * time.Second),
			}
		}},
	}
)

func (trace *statementTrace) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.HasPrefix(strings.TrimSpace(data.SQL), "EXPLAIN") {
		return ctx
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.sqls = append(trace.sqls, data.SQL)
	trace.args = append(trace.args, append([]any{}, data.Args...))
	return ctx
}

func (*statementTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (trace *statementTrace) reset() {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.sqls, trace.args = nil, nil
}

// TestOutboxClaimAtScale замеряет взятие на заранее засеянной базе (scripts/bench/outbox-seed.sql):
// план каждого запроса Claim и медиана времени настоящих взятий. Порогов нет — это замер.
func TestOutboxClaimAtScale(t *testing.T) {
	url := os.Getenv("OUTBOX_SCALE_DATABASE_URL")
	if url == "" {
		t.Skip("нужен OUTBOX_SCALE_DATABASE_URL засеянной базы")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	trace := &statementTrace{}
	config.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	shapes := outboxScaleShapes
	if os.Getenv("OUTBOX_SCALE_SHAPES") == "tenants" {
		shapes = tenantScaleShapes
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			measureClaim(ctx, t, pool, queue, trace, shape)
		})
	}
	t.Run("churn", func(t *testing.T) {
		measureChurn(ctx, t, queue)
	})
	t.Run("ready-lanes", func(t *testing.T) {
		trace.reset()
		started := time.Now()
		lanes, err := queue.ReadyLanes(ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("ReadyLanes: %v за %s", lanes, time.Since(started))
		explainTraced(ctx, t, pool, trace)
	})
}

func measureClaim(
	ctx context.Context,
	t *testing.T,
	pool *pgxpool.Pool,
	queue *postgres.TaskQueue,
	trace *statementTrace,
	shape outboxScaleShape,
) {
	durations := make([]time.Duration, 0, outboxScaleRuns)
	taken := 0
	for range outboxScaleRuns {
		trace.reset()
		started := time.Now()
		tasks, err := fake.TasksOf(queue.Claim(ctx, shape.request(time.Now())))
		if err != nil {
			t.Fatal(err)
		}
		durations = append(durations, time.Since(started))
		taken += len(tasks)
	}
	slices.Sort(durations)
	median, slowest := durations[len(durations)/2], durations[len(durations)-1]
	summary := "%s: взято %d за %d взятий, медиана %s, максимум %s, запросов на взятие %d"
	t.Logf(summary, shape.name, taken, outboxScaleRuns, median, slowest, len(trace.sqls))
	explainTraced(ctx, t, pool, trace)
}

// measureChurn — взятие пачки и удаление выполненных подряд: мёртвые строки головы очереди не должны
// разгонять стоимость следующих взятий.
func measureChurn(ctx context.Context, t *testing.T, queue *postgres.TaskQueue) {
	claims := make([]time.Duration, 0, outboxChurnPasses)
	resolves := make([]time.Duration, 0, outboxChurnPasses)
	done := 0
	for range outboxChurnPasses {
		started := time.Now()
		tasks, err := fake.TasksOf(queue.Claim(ctx, outboxScaleShapes[0].request(time.Now())))
		if err != nil {
			t.Fatal(err)
		}
		claims = append(claims, time.Since(started))
		results := make([]ports.TaskResolution, 0, len(tasks))
		for _, task := range tasks {
			results = append(results, ports.TaskResolution{ID: task.ID, Attempts: task.Attempts, At: time.Now()})
		}
		started = time.Now()
		if err := queue.Resolve(ctx, results); err != nil {
			t.Fatal(err)
		}
		resolves = append(resolves, time.Since(started))
		done += len(tasks)
	}
	first, last := claims[0], claims[len(claims)-1]
	slices.Sort(claims)
	slices.Sort(resolves)
	summary := "оборот: выполнено %d, взятие медиана %s (первое %s, последнее %s, максимум %s), отметка медиана %s"
	t.Logf(summary, done, claims[len(claims)/2], first, last, claims[len(claims)-1], resolves[len(resolves)/2])
}

// explainTraced повторяет последние запросы взятия под EXPLAIN ANALYZE в откатываемой транзакции.
func explainTraced(ctx context.Context, t *testing.T, pool *pgxpool.Pool, trace *statementTrace) {
	trace.mu.Lock()
	sqls, args := slices.Clone(trace.sqls), slices.Clone(trace.args)
	trace.mu.Unlock()
	for n, sql := range sqls {
		upper := strings.ToUpper(strings.TrimSpace(sql))
		if !strings.HasPrefix(upper, "WITH") && !strings.HasPrefix(upper, "SELECT") &&
			!strings.HasPrefix(upper, "UPDATE") && !strings.HasPrefix(upper, "DELETE") {
			continue
		}
		explainOne(ctx, t, pool, sql, args[n])
	}
}

func explainOne(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sql string, args []any) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + plan.String())
}
