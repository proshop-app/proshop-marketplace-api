//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const (
	seedFanoutTenants = `INSERT INTO tenants (tenant_id, slug, status, event_at, version, updated_at)
		SELECT id, id, 'active', $1, 1, $1 FROM (
			SELECT 't' || lpad(n::text, 3, '0') AS id FROM generate_series(1, 205) n
		) generated`
	seedFanoutParticipants = `INSERT INTO participants (tenant_id, slug, name, stage, version, created_at, updated_at)
		SELECT tenant_id, slug, 'Магазин', 'candidate', 1, $1, $1 FROM tenants`
	countFanoutTasks = `SELECT count(*) FROM outbox WHERE kind = $1`
	lockFanoutRules  = `SELECT revision FROM moderation_rules WHERE id = 1 FOR UPDATE`
)

func TestModerationFanoutContinuesWithoutRulesLock(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	for _, query := range []string{seedFanoutTenants, seedFanoutParticipants} {
		if _, err := pool.Exec(ctx, query, at(0)); err != nil {
			t.Fatal(err)
		}
	}
	uow := postgres.NewUnitOfWork(pool)
	service := appmoderation.NewService(uow, fake.NewClock(at(1)), &fake.IDs{}, appmoderation.Config{})
	if err := service.AddStopPhrase(ctx, "Товар", moderation.Actor{}); err != nil {
		t.Fatal(err)
	}
	count := func(kind string, want int) {
		t.Helper()
		var got int
		if err := pool.QueryRow(ctx, countFanoutTasks, kind).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: задач %d, нужно %d, ошибка %v", kind, got, want, err)
		}
	}
	count(appmoderation.TaskReevaluateAll, 1)
	count(appmoderation.TaskReevaluate, 0)
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	rules, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rules.Rollback(ctx) }()
	if _, err := rules.Exec(ctx, lockFanoutRules); err != nil {
		t.Fatal(err)
	}
	request := ports.ClaimRequest{
		Kinds:      []string{appmoderation.TaskReevaluateAll},
		Now:        at(1),
		LeaseUntil: at(2),
		Limit:      1,
		PerTenant:  1,
	}
	for _, want := range []int{200, 205} {
		pass, err := queue.Claim(ctx, request)
		if err != nil || len(pass.Tasks) != 1 {
			t.Fatal(pass, err)
		}
		task := pass.Tasks[0]
		deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = service.HandleReevaluateAll(deadline, task)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if err := queue.Complete(ctx, task.ID, task.Attempts, at(1)); err != nil {
			t.Fatal(err)
		}
		count(appmoderation.TaskReevaluate, want)
	}
	count(appmoderation.TaskReevaluateAll, 0)
}
