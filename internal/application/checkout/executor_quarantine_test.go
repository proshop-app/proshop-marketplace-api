package checkout_test

import (
	"context"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestExecutorQuarantinesMalformedTasks(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	err := e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().Enqueue(ctx, ports.NewTask{
			Kind:     appcheckout.TaskCreateOrder,
			TenantID: "t1",
			Payload:  []byte("{"),
			At:       e.clock.Now(),
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := appcheckout.NewExecutor(e.store, e.clock, e.service)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.Results():
	case <-time.After(time.Second):
		t.Fatal("некорректная задача не разобрана")
	}
	executor.Wait()
	e.clock.Advance(24 * time.Hour)
	tasks, err := e.store.Claim(ctx, ports.ClaimRequest{
		Now:        e.clock.Now(),
		Limit:      1,
		PerTenant:  1,
		LeaseUntil: e.clock.Now().Add(time.Minute),
	})
	if err != nil || len(tasks) != 0 {
		t.Fatal("полоса оформления повторяет постоянную ошибку", tasks, err)
	}
}
