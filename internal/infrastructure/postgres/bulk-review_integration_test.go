//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
)

const bulkFairSeed = `INSERT INTO outbox (kind, tenant_id, payload, next_at, created_at)
    SELECT 'fair', 'A', '{}', now(), now() FROM generate_series(1, 50000)`
const bulkFairQuiet = `INSERT INTO outbox (kind, tenant_id, payload, next_at, created_at)
    VALUES ('fair', 'B', '{}', now(), now())`

func TestBulkBacklogDoesNotOccupyDispatcherPass(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, bulkFairSeed); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var waiting time.Time
	var quietDelay time.Duration
	d := messaging.NewDispatcher(
		postgres.NewTaskQueue(postgres.Single(pool)),
		clock.System{},
		messaging.DispatcherConfig{
			Batch:     200,
			PerTenant: 200,
			Lease:     time.Minute,
		},
		map[string]messaging.Handler{
			"fair": func(ctx context.Context, task ports.Task) error {
				if task.TenantID == "B" {
					quietDelay = time.Since(waiting)
					return nil
				}
				if calls.Add(1) == 1 {
					waiting = time.Now()
					if _, err := pool.Exec(ctx, bulkFairQuiet); err != nil {
						return err
					}
				}
				time.Sleep(5 * time.Millisecond)
				return nil
			},
		},
	)
	started := time.Now()
	if _, _, _, err := d.RunWork(ctx, nil); err != nil {
		t.Fatal(err)
	}
	firstCalls, pass := calls.Load(), time.Since(started)
	if _, _, _, err := d.RunWork(ctx, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("A backlog=50000, first pass A=%d, pass=%s, B wait=%s", firstCalls, pass, quietDelay)
	if firstCalls != 8 || quietDelay == 0 {
		t.Fatalf("магазин A занял проход: %d задач, B ждал %s", firstCalls, quietDelay)
	}
}

func TestBulkMigrationDownResumesAfterDDLBeforeVersion(t *testing.T) {
	pool := testPool(t)
	raw, err := os.ReadFile("migrations/00062_moderation_bulk.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := strings.Split(string(raw), "-- +goose Down")[1]
	// DDL завершился, запись goose об откате не сохранилась: повторяем штатный Down.
	for _, statement := range strings.Split(down, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := pool.Exec(context.Background(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := postgres.MigrateDownTo(context.Background(), pool, 61); err != nil {
		t.Fatal("повтор Down после DDL", err)
	}
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
}

func TestBulkIdempotencyRetentionRemovesOldResults(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO moderation_bulk_idempotency (key_hash, response)
        SELECT n::text, '{}'::jsonb FROM generate_series(1, 3) n`); err != nil {
		t.Fatal(err)
	}
	future := time.Now().AddDate(1, 0, 0)
	if _, err := postgres.NewRetention(postgres.Single(pool)).Purge(ctx, ports.PurgeRequest{
		BulkBefore:               future,
		ProcessedBefore:          future,
		PurchaseQuarantineBefore: future,
		ParkedBefore:             future,
		Batch:                    2,
	}); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM moderation_bulk_idempotency").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("результаты не очищены: %d", remaining)
	}
}

func TestBulkWholeQueueFiltersCountsAndPages(t *testing.T) {
	pool, uow, service, selected := seedBulkApproval(t, 202, 1000)
	seedRevalidationMember(t, uow, "t2")
	ctx := context.Background()
	for _, table := range []string{"catalog_products", "moderation_verdicts", "moderation_cases"} {
		if _, err := pool.Exec(ctx, "UPDATE "+table+" SET tenant_id = 't2' WHERE product_id = 'p000'"); err != nil {
			t.Fatal(err)
		}
	}
	query := "UPDATE moderation_cases SET source = 'operator' WHERE id = $1"
	if _, err := pool.Exec(ctx, query, selected[201].ID); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		selector moderation.BulkSelector
		counts   moderation.BulkCounts
	}{
		{selector: moderation.BulkSelector{}, counts: moderation.BulkCounts{Total: 202, Tenants: 2, OtherReasons: 1}},
		{selector: moderation.BulkSelector{TenantID: "t2"}, counts: moderation.BulkCounts{Total: 1, Tenants: 1}},
		{
			selector: moderation.BulkSelector{Source: "operator", Reason: "moderation.probation"},
			counts:   moderation.BulkCounts{Total: 1, Tenants: 1, OtherReasons: 1},
		},
		{
			selector: moderation.BulkSelector{Reasons: []string{"moderation.probation"}},
			counts:   moderation.BulkCounts{Total: 201, Tenants: 2},
		},
	}
	for _, test := range tests {
		actual, err := service.BulkCount(ctx, test.selector)
		if err != nil || actual != test.counts {
			t.Fatal(test.selector, actual, err)
		}
	}
	selector := moderation.BulkSelector{}
	total := 0
	for page := range 2 {
		cmd := bulkApprovalCommand(nil, fmt.Sprintf("all-%d", page))
		cmd.Selector = &selector
		result, err := service.BulkAllow(ctx, cmd)
		if err != nil || result.Approved > 200 || result.Skipped != 0 {
			t.Fatal(result, err)
		}
		total += result.Approved
		selector.After = result.NextAfter
	}
	if total != 202 {
		t.Fatal(total)
	}
}

func TestBulkExpiredSelectorCannotExecuteAfterRetention(t *testing.T) {
	pool, uow, _, _ := seedBulkApproval(t, 3, 1000)
	now := fake.NewClock(time.Now())
	service := appmoderation.NewService(uow, now, random.IDs{}, appmoderation.Config{ProbationOffers: 1000})
	cmd := bulkApprovalCommand(nil, "expires")
	cmd.Selector = &moderation.BulkSelector{TenantID: "t1", Reasons: []string{"moderation.probation"}}
	first, err := service.BulkAllow(context.Background(), cmd)
	if err != nil || first.Approved != 2 {
		t.Fatal(first, err)
	}
	now.Advance(31 * 24 * time.Hour)
	purged, err := postgres.NewRetention(postgres.Single(pool)).Purge(context.Background(), ports.PurgeRequest{
		BulkBefore: now.Now(),
		Batch:      1,
	})
	if err != nil || purged.BulkApprovals != 1 {
		t.Fatal(purged, err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE moderation_cases
        SET reasons = '[{"key":"moderation.probation"}]' WHERE status = 'open'`); err != nil {
		t.Fatal(err)
	}
	_, err = service.BulkAllow(context.Background(), cmd)
	if !errors.Is(err, moderation.ErrKeyExpired) {
		t.Fatal("забытый ключ исполнил новый отбор", err)
	}
	counts, err := service.BulkCount(context.Background(), *cmd.Selector)
	if err != nil || counts.Total != 1 {
		t.Fatal(counts, err)
	}
}

func TestOutboxTenantCursorRotatesPastBusyStores(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO outbox (kind, tenant_id, payload, next_at, created_at)
        SELECT 'fair', 't' || (n % 8), '{}', now(), now() FROM generate_series(0, 23) n`); err != nil {
		t.Fatal(err)
	}
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	request := ports.ClaimRequest{
		Now:        time.Now().Add(time.Minute),
		Limit:      8,
		PerTenant:  1,
		LeaseUntil: time.Now().Add(2 * time.Minute),
	}
	first, err := fake.TasksOf(queue.Claim(ctx, request))
	if err != nil || len(first) != 8 {
		t.Fatal(first, err)
	}
	request.After = ports.ClaimAfter(first[len(first)-1].TenantID)
	if _, err := pool.Exec(ctx, bulkFairQuiet); err != nil {
		t.Fatal(err)
	}
	second, err := fake.TasksOf(queue.Claim(ctx, request))
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range second {
		if task.TenantID == "B" {
			return
		}
	}
	t.Fatal("старые магазины повторно вытеснили новый", second)
}
