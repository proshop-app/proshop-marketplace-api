//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func TestOutboxLanePauseIsSharedAndFenced(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	first := postgres.NewTaskQueue(postgres.Single(pool))
	second := postgres.NewTaskQueue(postgres.Single(pool))
	at := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().EnqueueBatch(ctx, []ports.NewTask{
			{Kind: "index", TenantID: "t1", Payload: []byte("{}"), At: at},
			{Kind: "index", TenantID: "t2", Payload: []byte("{}"), At: at},
			{Kind: "settings", TenantID: "t3", Payload: []byte("{}"), At: at},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	request := ports.ClaimRequest{
		Kinds:      []string{"index"},
		Now:        at,
		Limit:      1,
		PerTenant:  1,
		LeaseUntil: at.Add(time.Minute),
	}
	initial, err := first.Claim(ctx, request)
	if err != nil || len(initial.Tasks) != 1 {
		t.Fatal(initial, err)
	}
	task := initial.Tasks[0]
	retry := at.Add(time.Second)
	resume := at.Add(30 * time.Second)
	result := ports.TaskResolution{
		ID:         task.ID,
		Attempts:   task.Attempts,
		At:         at,
		NextAt:     &retry,
		PauseKey:   "search",
		PauseUntil: &resume,
		Error:      "поиск недоступен",
	}
	stale := result
	stale.Attempts++
	if err := first.Resolve(ctx, []ports.TaskResolution{stale}); !errors.Is(err, ports.ErrTaskClaimConflict) {
		t.Fatal(err)
	}
	request.Kinds = nil
	request.Gate = ports.TaskLaneGate{Key: "search", Kinds: []string{"index"}}
	request.Now = retry
	unpaused, err := second.Claim(ctx, request)
	if err != nil || len(unpaused.Tasks) != 1 || unpaused.Tasks[0].Kind != "index" {
		t.Fatal(unpaused, err)
	}
	if err := first.Resolve(ctx, []ports.TaskResolution{result}); err != nil {
		t.Fatal(err)
	}
	paused, err := second.Claim(ctx, request)
	if err != nil || len(paused.Tasks) != 1 || paused.Tasks[0].Kind != "settings" {
		t.Fatal(paused, err)
	}
	request.Now = resume
	recovered, err := second.Claim(ctx, request)
	if err != nil || len(recovered.Tasks) != 1 || recovered.Tasks[0].ID != task.ID {
		t.Fatal(recovered, err)
	}
}
