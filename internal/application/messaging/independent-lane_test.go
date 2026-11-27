package messaging_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

type countedClaims struct {
	*fake.Store
	calls int
}

func TestIndependentLaneCompletesOtherWorkWhileSearchWaits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	queue := &countedClaims{Store: fake.NewStore()}
	clock := fake.NewClock(t0)
	started := make(chan struct{})
	dispatcher := messaging.NewDispatcher(queue, clock, config(), map[string]messaging.Handler{
		"settings": func(context.Context, ports.Task) error { return nil },
	})
	dispatcher.RegisterBatch("index", func(ctx context.Context, _ []ports.Task) []error {
		close(started)
		<-ctx.Done()
		return nil
	})
	dispatcher.RegisterBatch("status", func(context.Context, []ports.Task) []error { return nil })
	gate := ports.TaskLaneGate{Key: "search", Kinds: []string{"index"}}
	lane := messaging.NewIndependentLane(
		dispatcher,
		gate,
		time.Minute,
		func(error) bool { return false },
		nil,
	)
	defer func() {
		cancel()
		lane.Wait()
	}()
	if outcomes, err := runLaneForTest(ctx, lane); err != nil || len(outcomes) != 0 || queue.calls != 2 {
		t.Fatalf("пустой проход: %v, %v, взятий %d", outcomes, err, queue.calls)
	}
	enqueue(t, queue.Store, "index", "t1", "index", t0)
	if outcomes, err := runLaneForTest(ctx, lane); err != nil || len(outcomes) != 0 {
		t.Fatal(outcomes, err)
	}
	<-started
	enqueue(t, queue.Store, "settings", "t2", "settings", t0)
	enqueue(t, queue.Store, "status", "t3", "status", t0)
	outcomes, err := runLaneForTest(ctx, lane)
	if err != nil || len(outcomes) != 2 || queue.calls != 6 {
		t.Fatalf("проход при занятом поиске: %v, %v, взятий %d", outcomes, err, queue.calls)
	}
	rows := queue.OutboxRows()
	if rows[0].DoneAt != nil || rows[1].DoneAt == nil || rows[2].DoneAt == nil {
		t.Fatal(rows)
	}
}

func TestIndependentLanePausesSearchOnOtherReplicaUntilClockReachesResume(t *testing.T) {
	ctx := context.Background()
	store, clock := fake.NewStore(), fake.NewClock(t0)
	failure := errs.New(errs.Unavailable, "test.search_unavailable", "поиск недоступен")
	enqueue(t, store, "index", "t1", "index1", t0)
	dispatcher := messaging.NewDispatcher(store, clock, config(), map[string]messaging.Handler{
		"index": func(context.Context, ports.Task) error { return failure },
	})
	gate := ports.TaskLaneGate{Key: "search", Kinds: []string{"index"}}
	lane := messaging.NewIndependentLane(
		dispatcher,
		gate,
		time.Minute,
		func(err error) bool { return errors.Is(err, failure) },
		func(context.Context) bool { return true },
	)
	if _, err := runLaneForTest(ctx, lane); err != nil {
		t.Fatal(err)
	}
	lane.Wait()

	var secondCalls atomic.Int32
	second := messaging.NewDispatcher(store, clock, config(), map[string]messaging.Handler{
		"index": func(context.Context, ports.Task) error { secondCalls.Add(1); return nil },
		"other": func(context.Context, ports.Task) error { return nil },
	})
	replica := messaging.NewIndependentLane(second, gate, time.Minute, func(error) bool { return false }, nil)
	enqueue(t, store, "index", "t2", "index2", t0)
	enqueue(t, store, "other", "t3", "other", t0)
	clock.Advance(59 * time.Second)
	outcomes, err := runLaneForTest(ctx, replica)
	if err != nil || len(outcomes) != 1 || outcomes[0].Task.Kind != "other" {
		t.Fatal(outcomes, err)
	}
	replica.Wait()
	if secondCalls.Load() != 0 {
		t.Fatalf("поиск вызван на второй реплике во время паузы: %d", secondCalls.Load())
	}
	clock.Advance(time.Second)
	if _, err := runLaneForTest(ctx, replica); err != nil {
		t.Fatal(err)
	}
	replica.Wait()
	if secondCalls.Load() != 2 {
		t.Fatalf("после паузы должны пройти обе задачи: %d", secondCalls.Load())
	}
}

func (q *countedClaims) Claim(ctx context.Context, req ports.ClaimRequest) (ports.ClaimResult, error) {
	q.calls++
	return q.Store.Claim(ctx, req)
}

func runLaneForTest(ctx context.Context, lane *messaging.IndependentLane) ([]messaging.Outcome, error) {
	previous, previousErr := lane.Finish()
	outcomes, _, _, err := lane.RunWork(ctx, nil)
	return append(previous, outcomes...), errors.Join(previousErr, err)
}
