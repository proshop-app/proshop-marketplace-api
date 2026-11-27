//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type drainQueue struct {
	ports.TaskQueue
	confirmed int
	finish    func(int)
}

func TestOutboxIndependentLaneDrainsIndexBacklogWithoutPoll(t *testing.T) {
	pool := testPool(t)
	clock := fake.NewClock(at(0))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tasks := make([]ports.NewTask, 450)
	for i := range tasks {
		tasks[i] = ports.NewTask{
			Kind:     "index",
			TenantID: ids.TenantID(fmt.Sprintf("t%04d", i)),
			Payload:  []byte("{}"),
			At:       clock.Now(),
		}
	}
	uow := postgres.NewUnitOfWork(pool)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().EnqueueBatch(ctx, tasks)
	})
	if err != nil {
		t.Fatal(err)
	}
	queue := &drainQueue{
		TaskQueue: postgres.NewTaskQueue(postgres.Single(pool)),
		finish: func(confirmed int) {
			if confirmed >= len(tasks) {
				cancel()
			}
		},
	}
	dispatch := messaging.NewDispatcher(
		queue,
		clock,
		messaging.DispatcherConfig{Batch: 200, PerTenant: 1},
		nil,
	)
	batches := 0
	dispatch.RegisterBatch("index", func(context.Context, []ports.Task) []error {
		batches++
		clock.Advance(10 * time.Millisecond)
		return nil
	})
	gate := ports.TaskLaneGate{Key: "search", Kinds: []string{"index"}}
	lane := messaging.NewIndependentLane(dispatch, gate, time.Second, func(error) bool { return false }, nil)
	registry := prometheus.NewRegistry()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := jobs.NewOutboxRunner(dispatch, time.Hour, logger, registry)
	if err := runner.RunIndependent(ctx, lane); err != nil {
		t.Fatal(err)
	}
	if queue.confirmed != len(tasks) || batches < 3 {
		t.Fatalf("подтверждено %d из %d задач за %d пачек", queue.confirmed, len(tasks), batches)
	}
}

func (q *drainQueue) Resolve(ctx context.Context, results []ports.TaskResolution) error {
	if err := q.TaskQueue.Resolve(ctx, results); err != nil {
		return err
	}
	q.confirmed += len(results)
	q.finish(q.confirmed)
	return nil
}

func (q *drainQueue) Complete(ctx context.Context, id int64, attempts int, at time.Time) error {
	if err := q.TaskQueue.Complete(ctx, id, attempts, at); err != nil {
		return err
	}
	q.confirmed++
	q.finish(q.confirmed)
	return nil
}
