//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const seedDeadlineCircle = `
	INSERT INTO outbox (kind, tenant_id, payload, next_at, created_at)
	SELECT $1, 'a' || lpad(n::text, 3, '0'), '{}', $3, $2 FROM generate_series(1, $4) n
`

type workSignals struct {
	hints []ports.WorkHints
}

func (s *workSignals) Notify(hints ports.WorkHints) {
	s.hints = append(s.hints, hints)
}

func TestWorkDeadlineFollowsOrderedHeadAndPause(t *testing.T) {
	pool := testPool(t)
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	now := at(0)
	kind := "catalog.tenant.sync"
	lease := now.Add(time.Minute)
	retry := now.Add(30 * time.Second)
	resume := now.Add(45 * time.Second)
	enqueue := func(tenant ids.TenantID, at time.Time) {
		t.Helper()
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			return tx.Outbox().Enqueue(ctx, ports.NewTask{Kind: kind, TenantID: tenant, At: at, Payload: []byte("{}")})
		}); err != nil {
			t.Fatal(err)
		}
	}
	enqueue("t1", now)
	request := ports.ClaimRequest{
		Kinds:        []string{kind},
		OrderedKinds: []string{kind},
		Now:          now,
		PerTenant:    1,
		Limit:        1,
		LeaseUntil:   lease,
		Gate:         ports.TaskLaneGate{Key: "search", Kinds: []string{kind}},
	}
	claimed, err := queue.Claim(ctx, request)
	if err != nil || len(claimed.Tasks) != 1 {
		t.Fatal(claimed, err)
	}
	enqueue("t1", now)
	enqueue("t2", lease.Add(time.Second))
	assertWorkDeadline(t, queue, request, lease)
	task := claimed.Tasks[0]
	err = queue.Resolve(ctx, []ports.TaskResolution{{
		ID:         task.ID,
		Attempts:   task.Attempts,
		At:         now,
		NextAt:     &retry,
		PauseKey:   "search",
		PauseUntil: &resume,
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertWorkDeadline(t, queue, request, resume)
	request.TenantSlots = map[ids.TenantID]int{"t1": 0}
	assertWorkDeadline(t, queue, request, lease.Add(time.Second))
	request.ExcludeKinds = []string{kind}
	assertWorkDeadline(t, queue, request, time.Time{})
}

func TestWorkDeadlineUsesBoundedClaimCircle(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	uow := postgres.NewUnitOfWork(pool)
	kind := "catalog.tenant.sync"
	now := at(0)
	ready := now.Add(time.Second)
	if _, err := pool.Exec(ctx, seedDeadlineCircle, kind, now, now.Add(time.Hour), ports.ClaimLaneVisits); err != nil {
		t.Fatal(err)
	}
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().EnqueueBatch(ctx, []ports.NewTask{
			{Kind: kind, TenantID: "a001", At: now, Payload: []byte("{}")},
			{Kind: kind, TenantID: "z", At: ready, Payload: []byte("{}")},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	request := ports.ClaimRequest{
		Kinds:        []string{kind},
		OrderedKinds: []string{kind},
		Now:          ready,
		PerTenant:    1,
		Limit:        1,
		LeaseUntil:   ready.Add(time.Minute),
	}
	// Готовая голова вне первых 128 полос. Чтение возвращает нижнюю границу и будит взятие.
	assertWorkDeadline(t, queue, request, now)
	claimed, err := queue.Claim(ctx, request)
	if err != nil || len(claimed.Tasks) != 0 || claimed.Next.Tenant != "a128" {
		t.Fatal(claimed, err)
	}
	request.After = claimed.Next
	assertWorkDeadline(t, queue, request, now)
	claimed, err = queue.Claim(ctx, request)
	if err != nil || len(claimed.Tasks) != 1 || claimed.Tasks[0].TenantID != "z" {
		t.Fatal(claimed, err)
	}
}

func assertWorkDeadline(t *testing.T, queue *postgres.TaskQueue, request ports.ClaimRequest, want time.Time) {
	t.Helper()
	deadlines, err := queue.NextWorkByKind(context.Background(), request)
	got := deadlines[request.Kinds[0]]
	if err != nil || !got.Equal(want) {
		t.Fatalf("ближайший срок %v, нужен %v: %v", got, want, err)
	}
}

func TestEnqueueSignalsOnlyCommittedChanges(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	signal := &workSignals{}
	uow := postgres.NewUnitOfWork(pool).WithWorkSignal(signal)
	queue := postgres.NewTaskQueue(postgres.Single(pool)).WithWorkSignal(signal)
	now := at(0)
	task := ports.NewTask{
		Kind:        "test.signal",
		At:          now,
		AvailableAt: now.Add(time.Second),
		Payload:     []byte("{}"),
		DedupKey:    "signal",
	}
	rollback := errors.New("откат")
	enqueue := func(outcome error) error {
		return uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			if err := tx.Outbox().Enqueue(ctx, task); err != nil {
				return err
			}
			return outcome
		})
	}
	if err := enqueue(rollback); !errors.Is(err, rollback) || len(signal.hints) != 0 {
		t.Fatal(err, signal.hints)
	}
	if err := enqueue(nil); err != nil {
		t.Fatal(err)
	}
	if err := enqueue(nil); err != nil {
		t.Fatal(err)
	}
	if len(signal.hints) != 1 || !signal.hints[0][task.Kind].Equal(task.AvailableAt) {
		t.Fatal(signal.hints)
	}
	request := ports.ClaimRequest{
		Kinds:      []string{task.Kind},
		Now:        task.AvailableAt,
		PerTenant:  1,
		Limit:      1,
		LeaseUntil: now.Add(time.Minute),
	}
	result, err := queue.Claim(ctx, request)
	if err != nil || len(result.Tasks) != 1 {
		t.Fatal(result, err)
	}
	taken := result.Tasks[0]
	retry := now.Add(2 * time.Second)
	if err := queue.Resolve(ctx, []ports.TaskResolution{{
		ID:       taken.ID,
		Attempts: taken.Attempts,
		At:       now,
		NextAt:   &retry,
	}}); err != nil {
		t.Fatal(err)
	}
	if len(signal.hints) != 2 || !signal.hints[1][task.Kind].Equal(retry) {
		t.Fatal(signal.hints)
	}
	assertWorkDeadline(t, queue, request, retry)
}
