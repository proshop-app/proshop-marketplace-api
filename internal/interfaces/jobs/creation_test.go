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
)

type creationRunnerQueue struct {
	ports.TaskQueue
	stop      context.CancelFunc
	kinds     []string
	creations int
}

func (q *creationRunnerQueue) Claim(_ context.Context, request ports.ClaimRequest) (ports.ClaimResult, error) {
	kind := request.Kinds[0]
	q.kinds = append(q.kinds, kind)
	if kind != appcheckout.TaskCreateOrder {
		return ports.ClaimResult{}, nil
	}
	if q.creations == 2 {
		q.stop()
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
	queue := &creationRunnerQueue{TaskQueue: fake.NewStore(), stop: stop}
	clock := fake.NewClock(time.Unix(0, 0)).Scheduler()
	executor := appcheckout.NewExecutor(queue, clock, &appcheckout.Service{}, nil, messaging.DefaultMaxAttempts)
	results := 0
	runner := NewCreationRunner(executor, slog.New(slog.NewTextHandler(io.Discard, nil)), func(messaging.Outcome) {
		results++
	})
	if err := runner.run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{
		appcheckout.TaskExpireCreation,
		appcheckout.TaskCancelOrder,
		appcheckout.TaskPollOrder,
		appcheckout.TaskCreateOrder,
		appcheckout.TaskCreateOrder,
		appcheckout.TaskCreateOrder,
	}
	if results != 2 || !slices.Equal(queue.kinds, want) {
		t.Fatal("результат должен пополнять только свою полосу", results, queue.kinds)
	}
}

func (q *creationRunnerQueue) Resolve(context.Context, []ports.TaskResolution) error { return nil }
