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

func TestClaimInterleavesTenantsBeforeGlobalLimit(t *testing.T) {
	store := fake.NewStore()
	for n := range 10 {
		enqueue(t, store, "ok", "t1", fmt.Sprintf("n%d", n), t0)
	}
	enqueue(t, store, "ok", "t2", "q1", t0.Add(time.Second))
	enqueue(t, store, "ok", "t2", "q2", t0.Add(time.Second))
	tasks, err := store.Claim(context.Background(), ports.ClaimRequest{
		Now:        t0.Add(time.Minute),
		Limit:      4,
		PerTenant:  4,
		LeaseUntil: t0.Add(2 * time.Minute),
	})
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
		_, err := store.Claim(ctx, ports.ClaimRequest{
			Now:        clock.Now(),
			Limit:      1,
			PerTenant:  1,
			LeaseUntil: clock.Now().Add(time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		return nil
	})
	outcomes, err := d.RunOnce(context.Background())
	if !errors.Is(err, ports.ErrTaskClaimConflict) || len(outcomes) != 0 {
		t.Fatalf("устаревшая пачка не должна подтверждаться: %+v, %v", outcomes, err)
	}
	for _, row := range store.OutboxRows() {
		if row.DoneAt != nil {
			t.Fatal("конфликт должен откатить всю пачку")
		}
	}
}

func TestReadyCountsExcludesLeaseDelayAndCompleted(t *testing.T) {
	store := fake.NewStore()
	for n, kind := range []string{"a", "a", "b"} {
		enqueue(t, store, kind, "t1", fmt.Sprintf("k%d", n), t0)
	}
	ctx := context.Background()
	tasks, err := store.Claim(ctx, ports.ClaimRequest{Now: t0, Limit: 2, PerTenant: 2, LeaseUntil: t0.Add(time.Minute)})
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
	counts, err := store.ReadyCounts(ctx, t0)
	if err != nil || len(counts) != 1 || counts["b"] != 1 {
		t.Fatal(counts, err)
	}
	_, err = store.Claim(ctx, ports.ClaimRequest{Now: t0, Limit: 1, PerTenant: 1, LeaseUntil: t0.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	counts, err = store.ReadyCounts(ctx, t0)
	if err != nil || len(counts) != 0 {
		t.Fatalf("аренда не считается готовой задачей: %+v, %v", counts, err)
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
		_, err := d.RunOnce(context.Background())
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
		if _, err := d.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if !quiet.Load() {
		t.Fatal("магазины с остатком очереди повторно заняли все слоты перед новым магазином")
	}
}
