//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestOutboxSingleKindClaimKeepsCursorWhenNothingIsDue(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(pool)
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	enqueue(t, uow, "t1", "k1", 0)
	enqueue(t, uow, "t2", "k2", 0)
	enqueue(t, uow, "t3", "k3", 2)
	request := ports.ClaimRequest{
		Now:        at(0),
		Limit:      1,
		PerTenant:  1,
		Kinds:      []string{"test"},
		LeaseUntil: at(0).Add(30 * time.Second),
	}

	first, err := queue.Claim(ctx, request)
	if err != nil || len(first.Tasks) != 1 {
		t.Fatal(first, err)
	}
	request.After = first.Next
	second, err := queue.Claim(ctx, request)
	if err != nil || len(second.Tasks) != 1 {
		t.Fatal(second, err)
	}
	request.After = ports.ClaimAfter("t0")
	delayed, err := queue.Claim(ctx, request)
	if err != nil || len(delayed.Tasks) != 0 || delayed.Next != request.After {
		t.Fatal("при одних арендах и отложенных задачах обход полос не запускается", delayed, err)
	}
	request.Kinds = []string{"checkout.expire_creation"}
	request.After = second.Next
	empty, err := queue.Claim(ctx, request)
	if err != nil || len(empty.Tasks) != 0 || empty.Next != second.Next {
		t.Fatal("пустой вид должен сохранить курсор", empty, err)
	}
}

func TestOutboxLeaseExcludesAnotherWorkerPastTenSeconds(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(pool)
	first := postgres.NewTaskQueue(postgres.Single(pool))
	second := postgres.NewTaskQueue(postgres.Single(pool))
	now := at(0)
	enqueue(t, uow, "t1", "k1", 0)
	request := ports.ClaimRequest{
		Now:        now,
		Limit:      1,
		PerTenant:  1,
		Kinds:      []string{"test"},
		LeaseUntil: now.Add(30 * time.Second),
	}
	claimed, err := first.Claim(ctx, request)
	if err != nil || len(claimed.Tasks) != 1 {
		t.Fatal(claimed, err)
	}
	request.Now = now.Add(11 * time.Second)
	busy, err := second.Claim(ctx, request)
	if err != nil || len(busy.Tasks) != 0 {
		t.Fatal("вторая реплика взяла работающую задачу", busy, err)
	}
	request.Now = request.LeaseUntil
	request.LeaseUntil = request.Now.Add(30 * time.Second)
	recovered, err := second.Claim(ctx, request)
	if err != nil || len(recovered.Tasks) != 1 ||
		recovered.Tasks[0].ID != claimed.Tasks[0].ID || recovered.Tasks[0].Attempts != 2 {
		t.Fatal("после аренды задачу должен взять новый владелец", recovered, err)
	}
}

func TestOutboxProbePrefersRecentShopWithSlots(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(pool)
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	for i := range ports.ClaimLaneVisits {
		enqueue(t, uow, "t1", fmt.Sprint(i), 0)
	}
	enqueue(t, uow, "t2", "k2", 0)
	enqueue(t, uow, "t3", "k3", 1)
	enqueue(t, uow, "t4", "k4", 2)
	request := ports.ClaimRequest{
		Now:          at(3),
		Limit:        1,
		PerTenant:    1,
		Kinds:        []string{"test"},
		LeaseUntil:   at(3).Add(30 * time.Second),
		TenantSlots:  map[ids.TenantID]int{"t1": 0, "t4": 0},
		PreferRecent: true,
	}
	result, err := queue.Claim(ctx, request)
	if err != nil || len(result.Tasks) != 1 || result.Tasks[0].TenantID != "t3" {
		t.Fatal("проба должна взять свежую задачу магазина со свободным слотом", result, err)
	}
}

func TestOutboxOldestClaimYieldsToRecentWhenDeprioritized(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(pool)
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	enqueue(t, uow, "t1", "k1", 0)
	enqueue(t, uow, "t2", "k2", 1)
	request := ports.ClaimRequest{
		Now:                  at(2),
		Limit:                2,
		PerTenant:            1,
		Kinds:                []string{"test"},
		LeaseUntil:           at(2).Add(30 * time.Second),
		PreferRecent:         true,
		DeprioritizedTenants: []ids.TenantID{"t1"},
	}
	healthy, err := queue.Claim(ctx, request)
	if err != nil || len(healthy.Tasks) != 1 || healthy.Tasks[0].TenantID != "t2" {
		t.Fatal("при наличии свежего кандидата старейшая задача молчащего магазина должна подождать", healthy, err)
	}
	remaining, err := queue.Claim(ctx, request)
	if err != nil || len(remaining.Tasks) != 1 || remaining.Tasks[0].TenantID != "t1" {
		t.Fatal("при отсутствии других магазинов нужно взять оставшуюся задачу", remaining, err)
	}
}
