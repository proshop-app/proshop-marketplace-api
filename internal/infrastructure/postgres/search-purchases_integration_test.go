//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/searchfeedback"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

type searchPurchaseClaim struct {
	tasks []ports.Task
	err   error
}

func TestSearchPurchasesBatchRollbackAndTwoClaimers(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	first, second := postgres.NewTaskQueue(pool), postgres.NewTaskQueue(pool)
	tasks := []ports.NewTask{
		{
			Kind:     searchfeedback.TaskPurchase,
			TenantID: "t1",
			DedupKey: "marketplace-paid:checkout-17:t1:o1",
			Payload:  []byte(`{"eventId":"marketplace-paid:checkout-17:t1:o1"}`),
			At:       at(0),
		},
		{
			Kind:     searchfeedback.TaskPurchase,
			TenantID: "t2",
			DedupKey: "marketplace-paid:checkout-17:t2:o2",
			Payload:  []byte(`{"eventId":"marketplace-paid:checkout-17:t2:o2"}`),
			At:       at(0),
		},
	}
	save := func(ctx context.Context, tx ports.Tx) error { return tx.Outbox().EnqueueBatch(ctx, tasks) }
	rollback := errors.New("откат транзакции")
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := save(ctx, tx); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if counts, err := first.ReadyCounts(ctx, at(1)); err != nil || len(counts) != 0 {
		t.Fatal(counts, err)
	}
	for range 2 {
		if err := uow.Do(ctx, save); err != nil {
			t.Fatal(err)
		}
	}
	request := ports.ClaimRequest{
		Now:        at(1),
		LeaseUntil: at(2),
		Limit:      2,
		PerTenant:  1,
		Kinds:      []string{searchfeedback.TaskPurchase},
	}
	claimed := claimSearchPurchases(ctx, request, first, second)
	seen := make(map[string]bool)
	for _, result := range claimed {
		if result.err != nil {
			t.Fatal(result.err)
		}
		for _, task := range result.tasks {
			var payload struct {
				EventID string `json:"eventId"`
			}
			if err := json.Unmarshal(task.Payload, &payload); err != nil || payload.EventID == "" ||
				seen[payload.EventID] || task.Attempts != 1 {
				t.Fatal("задача потеряна или выдана повторно", task, err)
			}
			seen[payload.EventID] = true
		}
	}
	if len(seen) != len(tasks) {
		t.Fatal("каждая группа должна дать одну задачу", seen)
	}
}

func claimSearchPurchases(
	ctx context.Context,
	request ports.ClaimRequest,
	queues ...ports.TaskQueue,
) []searchPurchaseClaim {
	results := make([]searchPurchaseClaim, len(queues))
	var workers sync.WaitGroup
	for index, queue := range queues {
		workers.Add(1)
		go func() {
			defer workers.Done()
			results[index].tasks, results[index].err = queue.Claim(ctx, request)
		}()
	}
	workers.Wait()
	return results
}
