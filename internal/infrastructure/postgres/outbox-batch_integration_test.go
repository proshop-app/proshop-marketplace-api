//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func TestOutboxBatchResolvesAtomicallyAndReportsReadyLanes(t *testing.T) {
	pool := testPool(t)
	uow, queue := postgres.NewUnitOfWork(pool), postgres.NewTaskQueue(postgres.Single(pool))
	ctx := context.Background()
	for n := range 3 {
		enqueue(t, uow, "t1", fmt.Sprintf("k%d", n), 0)
	}
	lanes, err := queue.ReadyLanes(ctx, at(1))
	want := ports.ReadyLane{Kind: "test", Tenants: 1, Oldest: at(0)}
	if err != nil || len(lanes) != 1 || lanes[0].Kind != want.Kind || lanes[0].Tenants != want.Tenants ||
		!lanes[0].Oldest.Equal(want.Oldest) {
		t.Fatal(lanes, err)
	}
	tasks, err := fake.TasksOf(queue.Claim(ctx, ports.ClaimRequest{
		Now:        at(1),
		Limit:      3,
		PerTenant:  3,
		LeaseUntil: at(2),
	}))
	if err != nil || len(tasks) != 3 {
		t.Fatal(tasks, err)
	}
	lanes, err = queue.ReadyLanes(ctx, at(1))
	if err != nil || len(lanes) != 1 || lanes[0].Tenants != 0 {
		t.Fatalf("аренды не считаются готовыми: %+v, %v", lanes, err)
	}
	next := at(5)
	results := []ports.TaskResolution{
		{ID: tasks[0].ID, Attempts: tasks[0].Attempts, At: at(1)},
		{ID: tasks[1].ID, Attempts: tasks[1].Attempts, At: at(1), NextAt: &next, Error: "сбой"},
		{ID: tasks[2].ID, Attempts: tasks[2].Attempts + 1, At: at(1)},
	}
	if err := queue.Resolve(ctx, results); !errors.Is(err, ports.ErrTaskClaimConflict) {
		t.Fatalf("неверный токен должен откатить пачку: %v", err)
	}
	for _, task := range tasks {
		state := readOutboxClaimState(t, pool, task.ID)
		if state.removed || state.lastError != nil || !state.nextAt.Equal(at(2)) {
			t.Fatalf("пачка сохранилась частично: %+v", state)
		}
	}
	results[2].Attempts = tasks[2].Attempts
	if err := queue.Resolve(ctx, results); err != nil {
		t.Fatal(err)
	}
	lanes, err = queue.ReadyLanes(ctx, at(3))
	if err != nil || len(lanes) != 1 || lanes[0].Tenants != 0 {
		t.Fatal(lanes, err)
	}
	lanes, err = queue.ReadyLanes(ctx, at(5))
	if err != nil || len(lanes) != 1 || lanes[0].Tenants != 1 || !lanes[0].Oldest.Equal(at(5)) {
		t.Fatalf("созревший повтор должен учитываться: %+v, %v", lanes, err)
	}
	if done := readOutboxClaimState(t, pool, tasks[0].ID); !done.removed {
		t.Fatalf("выполненная задача удаляется при отметке: %+v", done)
	}
	state := readOutboxClaimState(t, pool, tasks[1].ID)
	if state.removed || state.lastError == nil || *state.lastError != "сбой" {
		t.Fatalf("причина повтора должна сохраниться: %+v", state)
	}
}

func TestOutboxBatchInterleavesBeforeLimit(t *testing.T) {
	pool := testPool(t)
	uow, queue := postgres.NewUnitOfWork(pool), postgres.NewTaskQueue(postgres.Single(pool))
	for n := range 10 {
		enqueue(t, uow, "t1", fmt.Sprintf("n%d", n), 0)
	}
	for n := range 2 {
		enqueue(t, uow, "t2", fmt.Sprintf("q%d", n), 1)
	}
	tasks, err := fake.TasksOf(queue.Claim(context.Background(), ports.ClaimRequest{
		Now:        at(2),
		Limit:      4,
		PerTenant:  4,
		LeaseUntil: at(3),
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
