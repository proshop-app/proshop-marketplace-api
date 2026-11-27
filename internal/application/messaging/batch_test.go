package messaging_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type resolutionCountingQueue struct {
	ports.TaskQueue
	singles atomic.Int64
	batches atomic.Int64
}

func (q *resolutionCountingQueue) Complete(ctx context.Context, id int64, attempts int, at time.Time) error {
	q.singles.Add(1)
	return q.TaskQueue.Complete(ctx, id, attempts, at)
}

func (q *resolutionCountingQueue) Resolve(ctx context.Context, results []ports.TaskResolution) error {
	q.batches.Add(1)
	return q.TaskQueue.Resolve(ctx, results)
}

func TestClaimInterleavesTenantsBeforeGlobalLimit(t *testing.T) {
	store := fake.NewStore()
	for n := range 10 {
		enqueue(t, store, "ok", "t1", fmt.Sprintf("n%d", n), t0)
	}
	enqueue(t, store, "ok", "t2", "q1", t0.Add(time.Second))
	enqueue(t, store, "ok", "t2", "q2", t0.Add(time.Second))
	tasks, err := fake.TasksOf(store.Claim(context.Background(), ports.ClaimRequest{
		Now:        t0.Add(time.Minute),
		Limit:      4,
		PerTenant:  4,
		LeaseUntil: t0.Add(2 * time.Minute),
	}))
	if err != nil || len(tasks) != 4 {
		t.Fatal(tasks, err)
	}
	for n, tenant := range []string{"t1", "t2", "t1", "t2"} {
		if string(tasks[n].TenantID) != tenant {
			t.Fatalf("магазины должны чередоваться: %+v", tasks)
		}
	}
}

func TestBatchResolutionRollsBackOnStaleOwner(t *testing.T) {
	store, clock := fake.NewStore(), fake.NewClock(t0)
	for n := range 2 {
		enqueue(t, store, "ok", ids.TenantID(fmt.Sprintf("t%d", n)), fmt.Sprintf("k%d", n), t0)
	}
	d := messaging.NewDispatcher(store, clock, config(), nil)
	d.RegisterBatch("ok", func(ctx context.Context, tasks []ports.Task) []error {
		clock.Advance(2 * time.Minute)
		_, err := fake.TasksOf(store.Claim(ctx, ports.ClaimRequest{
			Now:        clock.Now(),
			Limit:      1,
			PerTenant:  1,
			LeaseUntil: clock.Now().Add(time.Minute),
		}))
		if err != nil {
			t.Fatal(err)
		}
		return nil
	})
	outcomes, _, _, err := d.RunWork(context.Background(), nil)
	if !errors.Is(err, ports.ErrTaskClaimConflict) || len(outcomes) != 0 {
		t.Fatalf("устаревшая пачка не должна подтверждаться: %+v, %v", outcomes, err)
	}
	for _, row := range store.OutboxRows() {
		if row.DoneAt != nil {
			t.Fatal("конфликт должен откатить всю пачку")
		}
	}
}

func TestReadyLanesExcludeLeaseDelayAndCompleted(t *testing.T) {
	store := fake.NewStore()
	for n, kind := range []string{"a", "a", "b"} {
		enqueue(t, store, kind, "t1", fmt.Sprintf("k%d", n), t0)
	}
	ctx := context.Background()
	tasks, err := fake.TasksOf(store.Claim(ctx, ports.ClaimRequest{
		Now:        t0,
		Limit:      2,
		PerTenant:  2,
		LeaseUntil: t0.Add(time.Minute),
	}))
	if err != nil {
		t.Fatal(err)
	}
	next := t0.Add(time.Hour)
	err = store.Resolve(ctx, []ports.TaskResolution{
		{ID: tasks[0].ID, Attempts: tasks[0].Attempts, At: t0},
		{ID: tasks[1].ID, Attempts: tasks[1].Attempts, At: t0, NextAt: &next, Error: "сбой"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lanes, err := store.ReadyLanes(ctx, t0)
	if err != nil || len(lanes) != 1 || lanes[0].Kind != "b" || lanes[0].Tenants != 1 || !lanes[0].Oldest.Equal(t0) {
		t.Fatal(lanes, err)
	}
	_, err = fake.TasksOf(store.Claim(ctx, ports.ClaimRequest{
		Now:        t0,
		Limit:      1,
		PerTenant:  1,
		LeaseUntil: t0.Add(time.Minute),
	}))
	if err != nil {
		t.Fatal(err)
	}
	lanes, err = store.ReadyLanes(ctx, t0)
	if err != nil || len(lanes) != 0 {
		t.Fatalf("аренда не считается готовой задачей: %+v, %v", lanes, err)
	}
}

func TestDispatcherIsolatesSlowTenantAndBoundsHandlerDuration(t *testing.T) {
	store := fake.NewStore()
	clock := fake.NewClock(t0)
	enqueue(t, store, "slow", "A", "a", t0)
	enqueue(t, store, "fast", "B", "b", t0)
	fast := make(chan struct{})
	slow := make(chan struct{})
	var slowExpired, fastBeforeDeadline atomic.Bool
	d := messaging.NewDispatcher(
		store,
		clock,
		messaging.DispatcherConfig{
			Batch:          200,
			Concurrency:    1,
			HandlerTimeout: 100 * time.Millisecond,
			Lease:          time.Minute,
		},
		map[string]messaging.Handler{
			"fast": func(context.Context, ports.Task) error {
				<-slow
				fastBeforeDeadline.Store(!slowExpired.Load())
				close(fast)
				return nil
			},
		},
	)
	d.RegisterBatch("slow", func(ctx context.Context, _ []ports.Task) []error {
		close(slow)
		<-ctx.Done()
		slowExpired.Store(true)
		return []error{ctx.Err()}
	})

	done := make(chan error, 1)
	go func() {
		_, _, _, err := d.RunWork(context.Background(), nil)
		done <- err
	}()
	select {
	case <-fast:
	case <-time.After(time.Second):
		t.Fatal("чужая задача заблокирована медленным магазином")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !fastBeforeDeadline.Load() {
		t.Fatal("быстрая задача ждала истечения срока чужого обработчика")
	}
	rows := store.OutboxRows()
	if rows[0].DoneAt != nil || rows[0].LastError != context.DeadlineExceeded.Error() || rows[1].DoneAt == nil {
		t.Fatal("просроченный обработчик должен повторяться, быстрый — завершиться", rows)
	}
}

func TestDispatcherRotatesTenantsWhenReadyStoresExceedSlots(t *testing.T) {
	store := fake.NewStore()
	clock := fake.NewClock(t0)
	for tenant := range 8 {
		for task := range 3 {
			enqueue(t, store, "work", ids.TenantID(fmt.Sprintf("t%d", tenant)), fmt.Sprintf("%d:%d", tenant, task), t0)
		}
	}
	var once sync.Once
	var quiet atomic.Bool
	d := messaging.NewDispatcher(
		store,
		clock,
		messaging.DispatcherConfig{
			Batch:       200,
			Concurrency: 8,
			Lease:       time.Minute,
		},
		map[string]messaging.Handler{
			"work": func(_ context.Context, task ports.Task) error {
				once.Do(func() { enqueue(t, store, "work", "quiet", "quiet", t0) })
				if task.TenantID == "quiet" {
					quiet.Store(true)
				}
				return nil
			},
		},
	)
	for range 2 {
		if _, _, _, err := d.RunWork(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	if !quiet.Load() {
		t.Fatal("магазины с остатком очереди повторно заняли все слоты перед новым магазином")
	}
}

func TestDispatcherFillsFreeSlotsFromOneTenant(t *testing.T) {
	store := fake.NewStore()
	for n := range 16 {
		enqueue(t, store, "point", "A", fmt.Sprint(n), t0)
	}
	var active, peak atomic.Int64
	d := messaging.NewDispatcher(
		store,
		fake.NewClock(t0),
		messaging.DispatcherConfig{Concurrency: 8},
		map[string]messaging.Handler{
			"point": func(context.Context, ports.Task) error {
				n := active.Add(1)
				for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
				}
				time.Sleep(20 * time.Millisecond)
				active.Add(-1)
				return nil
			},
		},
	)
	d.RegisterBatch("unused", func(context.Context, []ports.Task) []error { return nil })
	outcomes, _, _, err := d.RunWork(context.Background(), nil)
	if err != nil || len(outcomes) != 8 || peak.Load() != 8 {
		t.Fatalf("свободные слоты простаивают: tasks=%d peak=%d err=%v", len(outcomes), peak.Load(), err)
	}
}

func TestDispatcherFillsSlotsBesideBatchOfSameTenant(t *testing.T) {
	store := fake.NewStore()
	enqueue(t, store, "batch", "A", "batch", t0)
	for n := range 8 {
		enqueue(t, store, "point", "A", fmt.Sprint(n), t0)
	}
	d := messaging.NewDispatcher(
		store,
		fake.NewClock(t0),
		messaging.DispatcherConfig{Concurrency: 8},
		map[string]messaging.Handler{
			"point": func(context.Context, ports.Task) error { return nil },
		},
	)
	d.RegisterBatch("batch", func(context.Context, []ports.Task) []error { return nil })
	outcomes, _, _, err := d.RunWork(context.Background(), nil)
	if err != nil || len(outcomes) != 8 {
		t.Fatal("пакет магазина не должен оставлять его точечные слоты пустыми", len(outcomes), err)
	}
}

func TestDispatcherResolvesPointPassInOneBatch(t *testing.T) {
	store := fake.NewStore()
	for n := range 8 {
		enqueue(t, store, "work", "A", fmt.Sprint(n), t0)
	}
	queue := &resolutionCountingQueue{TaskQueue: store}
	d := messaging.NewDispatcher(queue, fake.NewClock(t0), config(), map[string]messaging.Handler{
		"work": func(context.Context, ports.Task) error { return nil },
	})
	outcomes, _, _, err := d.RunWork(context.Background(), nil)
	if err != nil || len(outcomes) != 8 || queue.batches.Load() != 1 || queue.singles.Load() != 0 {
		t.Fatalf("tasks=%d batch=%d single=%d err=%v", len(outcomes), queue.batches.Load(), queue.singles.Load(), err)
	}
}
