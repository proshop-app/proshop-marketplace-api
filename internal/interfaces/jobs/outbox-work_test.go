package jobs

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

type outboxWorkProbe struct {
	deadlines map[string]time.Time
	reads     [][]string
	claims    [][]string
}

func (p *outboxWorkProbe) WorkRequest() ports.ClaimRequest { return ports.ClaimRequest{} }

func (p *outboxWorkProbe) RunWork(_ context.Context, kinds []string) ([]messaging.Outcome, []string, bool, error) {
	p.claims = append(p.claims, slices.Clone(kinds))
	for _, kind := range kinds {
		delete(p.deadlines, kind)
	}
	return nil, kinds, false, nil
}

func (p *outboxWorkProbe) NextWorkByKind(
	_ context.Context, request ports.ClaimRequest,
) (map[string]time.Time, error) {
	p.reads = append(p.reads, slices.Clone(request.Kinds))
	deadlines := make(map[string]time.Time)
	for kind, at := range p.deadlines {
		if len(request.Kinds) == 0 || slices.Contains(request.Kinds, kind) {
			deadlines[kind] = at
		}
	}
	return deadlines, nil
}

func TestOutboxCollectsSignalsBeforeClaimAndKeepsOtherDeadlines(t *testing.T) {
	ctx := context.Background()
	clock := fake.NewClock(time.Unix(1, 0))
	retryAt := clock.Now().Add(time.Minute)
	p := &outboxWorkProbe{deadlines: map[string]time.Time{"retry": retryAt}}
	r := &outboxCycle{
		runner:     NewOutboxRunner(nil, prometheus.NewRegistry(), clock),
		source:     p,
		dispatcher: p,
		work:       make(workSchedule),
	}
	r.recoverAll(ctx)

	p.deadlines["index"] = clock.Now()
	r.onSignal(ctx, ports.WorkHints{"index": clock.Now()})
	clock.Advance(80 * time.Millisecond)
	p.deadlines["offer"] = clock.Now()
	r.onSignal(ctx, ports.WorkHints{"index": clock.Now(), "offer": clock.Now()})
	r.onDue(ctx)
	if len(p.reads) != 1 || len(p.claims) != 0 {
		t.Fatalf("до конца окна: чтения %v, взятия %v", p.reads, p.claims)
	}

	clock.Advance(outboxBatchWindow)
	r.onDue(ctx)
	want := []string{"index", "offer"}
	if len(p.reads) != 2 || !slices.Equal(p.reads[1], want) ||
		len(p.claims) != 1 || !slices.Equal(p.claims[0], want) {
		t.Fatalf("чтения %v, взятия %v, нужны только %v", p.reads, p.claims, want)
	}
	if got := r.work.nearest(); !got.Equal(retryAt) {
		t.Fatalf("срок повтора %v, нужен %v", got, retryAt)
	}

	r.onSignal(ctx, ports.WorkHints{"": {}})
	clock.Advance(WorkRecovery)
	r.onDue(ctx)
	if len(p.reads) != 4 || len(p.reads[2]) != 0 || len(p.reads[3]) != 0 || len(p.claims) != 1 {
		t.Fatalf("чтение всех видов после переподключения и по страховке: %v, взятия %v", p.reads, p.claims)
	}
}

func TestWorkRetryIncreasesToLimitAndResetsAfterClaim(t *testing.T) {
	now := time.Unix(1, 0)
	s := workSchedule{"test": {at: now}}
	for _, want := range []time.Duration{25, 50, 100, 200, 400, 800, 1000, 1000} {
		s.afterAttempt([]string{"test"}, nil, now, nil)
		if at := s.nearest(); !at.Equal(now.Add(want * time.Millisecond)) {
			t.Fatalf("пауза %v, нужна %v мс", at.Sub(now), want)
		}
	}
	s.afterAttempt([]string{"test"}, []string{"test"}, now, nil)
	if at := s.nearest(); !at.Equal(now) {
		t.Fatalf("после успешного взятия осталась пауза до %v", at)
	}
	s.afterAttempt([]string{"test"}, nil, now, nil)
	if at := s.nearest(); !at.Equal(now.Add(25 * time.Millisecond)) {
		t.Fatalf("новая серия началась с %v", at.Sub(now))
	}
}

func TestOutboxRefillsAfterDetachedBatch(t *testing.T) {
	ctx := context.Background()
	clock := fake.NewClock(time.Unix(1, 0))
	store := fake.NewStore()
	kind := "index"
	if err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().Enqueue(ctx, ports.NewTask{Kind: kind, At: clock.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher := messaging.NewDispatcher(
		store,
		clock,
		messaging.DispatcherConfig{Batch: 20, PerTenant: 20},
		nil,
	)
	dispatcher.RegisterBatch(kind, func(context.Context, []ports.Task) []error { return nil })
	lane := messaging.NewIndependentLane(
		dispatcher,
		ports.TaskLaneGate{Kinds: []string{kind}},
		time.Second,
		func(error) bool { return false },
		nil,
	)
	source := &outboxWorkProbe{deadlines: map[string]time.Time{kind: clock.Now()}}
	cycle := outboxCycle{
		runner:     NewOutboxRunner(nil, prometheus.NewRegistry(), clock),
		dispatcher: lane,
		source:     source,
		lane:       lane,
		work:       make(workSchedule),
	}
	cycle.recoverAll(ctx)
	cycle.onDue(ctx)
	done := lane.Done()
	if done == nil {
		t.Fatal("индексная пачка не взята")
	}
	<-done

	clock.Advance(10 * time.Millisecond)
	source.deadlines[kind] = clock.Now()
	cycle.onResult(ctx)
	want := clock.Now()
	if got := cycle.nearest(); !isDue(got, want) {
		t.Fatalf("новая пачка на %v, нужно сразу в %v", got, want)
	}
}

// Run разбирает задачи по подсказкам и по сохранённым срокам.
func (r *OutboxRunner) Run(
	ctx context.Context, dispatcher OutboxDispatcher, source WorkDeadlines, watch ports.WorkWatch,
) error {
	cycle := outboxCycle{runner: r, dispatcher: dispatcher, source: source, work: make(workSchedule)}
	return cycle.run(ctx, watch)
}
