//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/searchfeedback"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type searchPurchaseClaim struct {
	tasks []ports.Task
	err   error
}

type paidSearchSeller struct {
	*fake.Platform
}

func (paidSearchSeller) GetOrder(_ context.Context, _ ids.TenantID, id string) (checkout.SellerOrder, error) {
	return checkout.SellerOrder{OrderID: id, Status: checkout.GroupPaid}, nil
}

func TestSearchPurchaseInsertFailurePreservesPaidCheckout(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	purchase := seedCheckoutList(t, uow)[3]
	// PostgreSQL прерывает транзакцию при любом таком INSERT.
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox ADD CHECK (kind <> 'search.purchase')`); err != nil {
		t.Fatal(err)
	}
	service := appcheckout.NewService(
		uow,
		paidSearchSeller{fake.NewPlatform()},
		nil,
		nil,
		&fake.IDs{},
		fake.NewClock(at(12)),
		appcheckout.Config{SearchBuyerKeys: searchfeedback.NewBuyerKeys(strings.Repeat("k", 32))},
	)
	payload, err := json.Marshal(map[string]ids.UUID{
		"BuyerID":    purchase.BuyerID(),
		"CheckoutID": purchase.ID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range purchase.Groups() {
		task := ports.Task{Kind: appcheckout.TaskPollOrder, TenantID: group.TenantID, Payload: payload}
		if err := service.HandleOrderTask(ctx, task); err != nil {
			t.Fatal("ошибка сигнала спроса дошла до сценария оплаты", err)
		}
	}
	saved, err := service.Get(ctx, purchase.BuyerID(), purchase.ID())
	if err != nil || saved.Status != checkout.StatusPaid {
		t.Fatal("оплата не сохранилась", saved.Status, err)
	}
}

func TestSearchPurchaseQuarantineUsesOutboxRetention(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	for _, key := range []string{"old", "recent"} {
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			return tx.Outbox().Enqueue(ctx, ports.NewTask{
				Kind:     searchfeedback.TaskPurchase,
				TenantID: "t1",
				DedupKey: key,
				Payload:  []byte(`{"buyerKey":"opaque"}`),
				At:       at(0),
			})
		}); err != nil {
			t.Fatal(err)
		}
	}
	enqueue(t, uow, "t1", "keep-other-kind", 0)
	tasks, err := queue.Claim(ctx, ports.ClaimRequest{Now: at(1), Limit: 3, PerTenant: 3, LeaseUntil: at(2)})
	if err != nil || len(tasks) != 3 {
		t.Fatal(tasks, err)
	}
	for i, task := range tasks {
		failedAt := at(2)
		if i == 1 {
			failedAt = at(12)
		}
		if err := queue.Resolve(ctx, []ports.TaskResolution{{
			ID:            task.ID,
			Attempts:      task.Attempts,
			At:            failedAt,
			FailureReason: "test.invalid",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	retention := postgres.NewRetention(postgres.Single(pool))
	result, err := retention.Purge(ctx, ports.PurgeRequest{OutboxDoneBefore: at(10), Batch: 1})
	if err != nil || result.OutboxDone != 1 {
		t.Fatal("истёкшая покупка должна быть удалена", result, err)
	}
	failures, err := queue.Failures(ctx, 0, 10)
	if err != nil || len(failures) != 2 || failures[0].ID != tasks[1].ID || failures[1].ID != tasks[2].ID {
		t.Fatal("свежая покупка и чужой вид задачи должны сохраниться", failures, err)
	}
}

func TestSearchPurchasesBatchRollbackAndTwoClaimers(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	first, second := postgres.NewTaskQueue(postgres.Single(pool)), postgres.NewTaskQueue(postgres.Single(pool))
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
