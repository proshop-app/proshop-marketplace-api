package jobs

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type creationRunnerQueue struct {
	ports.TaskQueue
	kinds     []string
	creations int
	clock     ports.Clock
}

type creationGateProbe struct {
	at time.Time
}

type creationDeadlineProbe struct {
	ports.TaskQueue
	at     time.Time
	claims []string
}

func (q *creationRunnerQueue) Claim(_ context.Context, request ports.ClaimRequest) (ports.ClaimResult, error) {
	kind := request.Kinds[0]
	q.kinds = append(q.kinds, kind)
	if kind != appcheckout.TaskCreateOrder {
		return ports.ClaimResult{}, nil
	}

	q.creations++
	return ports.ClaimResult{
		Tasks: []ports.Task{
			{ID: int64(q.creations), Kind: kind, TenantID: "t1", Payload: []byte("[")},
		},
	}, nil
}

func TestCreationRunnerRefillsOnlyResultLane(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	clock := fake.NewClock(time.Unix(0, 0)).Scheduler()
	queue := &creationRunnerQueue{TaskQueue: fake.NewStore(), clock: clock}
	executor := appcheckout.NewExecutor(queue, clock, &appcheckout.Service{}, nil, messaging.DefaultMaxAttempts)
	results := 0
	record := func(messaging.Outcome) {
		results++
		if results == 2 {
			stop()
		}
	}
	runner := NewCreationRunner(executor, slog.New(slog.NewTextHandler(io.Discard, nil)), record, clock)
	if err := runner.Run(ctx, queue, queue); err != nil {
		t.Fatal(err)
	}
	want := []string{
		appcheckout.TaskCreateOrder,
		appcheckout.TaskCreateOrder,
	}
	if results != 2 || !slices.Equal(queue.kinds, want) {
		t.Fatal("результат должен пополнять только свою полосу", results, queue.kinds)
	}
}

func (q *creationRunnerQueue) Resolve(context.Context, []ports.TaskResolution) error { return nil }

func (q *creationRunnerQueue) NextWorkByKind(
	_ context.Context, request ports.ClaimRequest,
) (map[string]time.Time, error) {
	if len(request.Kinds) > 1 {
		return map[string]time.Time{appcheckout.TaskCreateOrder: q.clock.Now()}, nil
	}
	return nil, nil
}

func (q *creationRunnerQueue) Changed() <-chan struct{} { return nil }

func (q *creationRunnerQueue) Take() ports.WorkHints { return nil }

func (p *creationGateProbe) ServerState() ports.PlatformServerState {
	if p.at.IsZero() {
		return ports.PlatformServerReady
	}
	return ports.PlatformServerBlocked
}

func (p *creationGateProbe) ServerWorkAt() time.Time { return p.at }

func (*creationGateProbe) CreationCandidates(bool) (bool, []ids.TenantID) { return false, nil }

func (p *creationDeadlineProbe) Claim(_ context.Context, request ports.ClaimRequest) (ports.ClaimResult, error) {
	p.claims = append(p.claims, request.Kinds...)
	return ports.ClaimResult{}, nil
}

func (p *creationDeadlineProbe) NextWorkByKind(
	context.Context, ports.ClaimRequest,
) (map[string]time.Time, error) {
	return map[string]time.Time{appcheckout.TaskCreateOrder: p.at}, nil
}

func TestCreationSignalReplacesBlockedDeadline(t *testing.T) {
	ctx := context.Background()
	clock := fake.NewClock(time.Unix(0, 0)).Scheduler()
	gate := &creationGateProbe{at: clock.Now().Add(5 * time.Second)}
	queue := &creationDeadlineProbe{at: clock.Now()}
	executor := appcheckout.NewExecutor(queue, clock, nil, gate, messaging.DefaultMaxAttempts)
	runner := NewCreationRunner(executor, nil, nil, clock)
	cycle := creationCycle{
		runner: runner,
		source: queue,
		kinds:  executor.WorkKinds(),
		work:   make(workSchedule),
	}
	cycle.recoverAll(ctx)
	if at := cycle.nearest(); !at.Equal(gate.at) {
		t.Fatalf("полоса ждёт до %v, нужен срок %v", at, gate.at)
	}

	clock.Advance(100 * time.Millisecond)
	gate.at = time.Time{}
	cycle.onSignal(ctx, ports.WorkHints{appcheckout.TaskCreateOrder: {}})
	if at := cycle.nearest(); at.After(clock.Now()) {
		t.Fatalf("после сигнала полоса всё ещё ждёт до %v", at)
	}
	cycle.onDue(ctx)
	if !slices.Equal(queue.claims, []string{appcheckout.TaskCreateOrder}) {
		t.Fatalf("взяты виды %v", queue.claims)
	}
}
