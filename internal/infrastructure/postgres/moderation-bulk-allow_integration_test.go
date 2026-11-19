//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	bulkChangedProducts = `UPDATE catalog_products
        SET revision = CASE WHEN product_id = 'p000' THEN revision + 1 ELSE revision END,
            deleted = product_id = 'p001',
            card = CASE WHEN product_id = 'p002' THEN jsonb_set(card, '{publication,published}', 'false') ELSE card END
        WHERE tenant_id = 't1' AND product_id IN ('p000', 'p001', 'p002')`
	bulkApprovalPlan = `EXPLAIN (ANALYZE, BUFFERS) SELECT id, version FROM moderation_cases
        WHERE tenant_id = 't1' AND status = 'open'
        AND moderation_reason_keys(reasons) = ARRAY['moderation.probation']::text[]
        AND id > '00000000-0000-0000-0000-000000000000'::uuid ORDER BY id LIMIT 200`
	bulkContinuation = `SELECT payload FROM outbox WHERE kind = 'moderation.tenant.allow_trusted'
        AND payload->>'after' IS NOT NULL ORDER BY id DESC LIMIT 1`
	bulkApprovalCounts = `SELECT (SELECT count(*) FROM moderation_decisions),
        approved_offers, version FROM participants WHERE tenant_id = 't1'`
)

func seedBulkApproval(t *testing.T, count, threshold int) (
	*pgxpool.Pool,
	*postgres.UnitOfWork,
	*appmoderation.Service,
	[]moderation.BulkCase,
) {
	t.Helper()
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	prepareModeratedRevalidation(t, uow, count)
	service := appmoderation.NewService(uow, clock.System{}, random.IDs{}, appmoderation.Config{
		ProbationOffers: threshold,
	})
	selected := []moderation.BulkCase{}
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		for i := range count {
			product := ids.ProductID(fmt.Sprintf("p%03d", i))
			v := moderation.NewVerdict("t1", product)
			policy := ""
			if i == count-1 {
				policy = "manual"
			}
			input := moderation.Input{
				Probation: true,
				Policy:    policy,
			}
			v.Evaluate(1, 1, moderation.Rules{Revision: 1}, input, time.Now())
			if err := tx.ModerationVerdicts().Save(ctx, v); err != nil {
				return err
			}
			c := moderation.NewCase(random.IDs{}.NewUUID(), v.State(), moderation.Rule, "", time.Now(), nil)
			if err := tx.ModerationCases().Save(ctx, c); err != nil {
				return err
			}
			selected = append(selected, moderation.BulkCase{ID: c.State().ID, ExpectedVersion: c.State().Version})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return pool, uow, service, selected
}

func bulkApprovalCommand(cases []moderation.BulkCase, key string) appmoderation.BulkCommand {
	return appmoderation.BulkCommand{
		Cases:          cases,
		Note:           "Проверено пачкой",
		Operator:       moderation.Actor{ID: "o1", Name: "Оператор"},
		IdempotencyKey: key,
	}
}

func TestBulkApprovalBatchReplayAndVersions(t *testing.T) {
	pool, _, service, cases := seedBulkApproval(t, 200, 1000)
	cases[0].ExpectedVersion++
	ctx := context.Background()
	cmd := bulkApprovalCommand(cases, "batch")
	result, err := service.BulkAllow(ctx, cmd)
	if err != nil || result.Approved != 199 || result.Skipped != 1 || result.Details[0].Reason != "changed" {
		t.Fatal(result, err)
	}
	replay, err := service.BulkAllow(ctx, cmd)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatal(replay, err)
	}
	cmd.Note = "Другая заметка"
	if _, err := service.BulkAllow(ctx, cmd); !errors.Is(err, moderation.ErrKeyReused) {
		t.Fatal(err)
	}
	var decisions, approved, version int
	if err := pool.QueryRow(ctx, bulkApprovalCounts).Scan(&decisions, &approved, &version); err != nil {
		t.Fatal(err)
	}
	if decisions != 199 || approved != 199 || version != 2 {
		t.Fatal(decisions, approved, version)
	}
	cmd = bulkApprovalCommand(cases, "already")
	result, err = service.BulkAllow(ctx, cmd)
	if err != nil || result.Approved != 0 || result.Skipped != 200 {
		t.Fatal(result, err)
	}
}

func TestBulkApprovalConcurrentOperators(t *testing.T) {
	pool, _, service, cases := seedBulkApproval(t, 200, 1000)
	var wg sync.WaitGroup
	results := make([]moderation.BulkResult, 2)
	failures := make([]error, 2)
	start := make(chan struct{})
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			cmd := bulkApprovalCommand(cases, "race")
			cmd.Operator.ID = fmt.Sprintf("o%d", i)
			results[i], failures[i] = service.BulkAllow(context.Background(), cmd)
		}()
	}
	close(start)
	wg.Wait()
	if failures[0] != nil || failures[1] != nil {
		t.Fatal(failures)
	}
	if results[0].Approved+results[1].Approved != 200 || results[0].Skipped+results[1].Skipped != 200 {
		t.Fatal(results)
	}
	var decisions, approved, version int
	row := pool.QueryRow(context.Background(), bulkApprovalCounts)
	if err := row.Scan(&decisions, &approved, &version); err != nil {
		t.Fatal(err)
	}
	if decisions != 200 || approved != 200 || version != 2 {
		t.Fatal(decisions, approved, version)
	}
}

func TestBulkApprovalSelectorAndPlan(t *testing.T) {
	pool, _, service, _ := seedBulkApproval(t, 402, 1000)
	ctx := context.Background()
	selector := moderation.BulkSelector{TenantID: "t1", Reasons: []string{"moderation.probation"}}
	count, err := service.BulkCount(ctx, selector)
	if err != nil || count != 401 {
		t.Fatal(count, err)
	}
	if _, err := pool.Exec(ctx, "ANALYZE moderation_cases"); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, bulkApprovalPlan)
	if err != nil {
		t.Fatal(err)
	}
	plan := ""
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan += line + "\n"
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Log(plan)
	if !strings.Contains(plan, "moderation_case_probation") {
		t.Fatal("не использован индекс", plan)
	}
	approved := 0
	for page := 0; ; page++ {
		cmd := bulkApprovalCommand(nil, fmt.Sprintf("page%d", page))
		cmd.Selector = &selector
		result, err := service.BulkAllow(ctx, cmd)
		if err != nil {
			t.Fatal(result, err)
		}
		approved += result.Approved
		if !result.HasMore {
			break
		}
		selector.After = result.NextAfter
	}
	if approved != 401 {
		t.Fatal(approved)
	}
	count, err = service.BulkCount(ctx, selector)
	if err != nil || count != 0 {
		t.Fatal(count, err)
	}
	remaining, err := service.ListCases(ctx, moderation.Filter{TenantID: "t1", Status: moderation.Open, Limit: 10})
	if err != nil || remaining.Total != 1 || len(remaining.Items[0].Reasons) != 2 {
		t.Fatal(remaining, err)
	}
}

func TestBulkApprovalAutomaticTrust(t *testing.T) {
	for _, mode := range []string{"single", "bulk", "manual"} {
		t.Run(mode, func(t *testing.T) {
			pool, uow, service, cases := seedBulkApproval(t, 205, 1)
			ctx := context.Background()
			switch mode {
			case "manual":
				members := appparticipant.NewService(uow, clock.System{}, random.IDs{})
				if _, err := members.SetTrust(ctx, "t1", participant.Trusted, participant.Decision{
					Note: "Проверен магазин",
				}); err != nil {
					t.Fatal(err)
				}
			case "bulk":
				if _, err := service.BulkAllow(ctx, bulkApprovalCommand(cases[:2], "promote")); err != nil {
					t.Fatal(err)
				}
			default:
				if _, err := service.Allow(ctx, appmoderation.DecisionCommand{
					CaseID:          cases[0].ID,
					ExpectedVersion: cases[0].ExpectedVersion,
					Note:            "Проверено",
				}); err != nil {
					t.Fatal(err)
				}
			}
			var pending int
			if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				var err error
				pending, err = tx.BulkModeration().Count(ctx, moderation.BulkSelector{
					TenantID: "t1",
					Reasons:  []string{"moderation.probation"},
				})
				return err
			}); err != nil || pending < 200 {
				t.Fatal("одобрение выполнено в запросе", pending, err)
			}
			task := appmoderation.AllowTrustedTask("t1", "", time.Now())
			if err := service.HandleTask(ctx, ports.Task{
				Kind:     task.Kind,
				TenantID: task.TenantID,
				Payload:  task.Payload,
			}); err != nil {
				t.Fatal(err)
			}
			var continuation ports.NewTask
			if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				selector := moderation.BulkSelector{
					TenantID: "t1",
					Reasons:  []string{"moderation.probation"},
				}
				page, err := tx.BulkModeration().Page(ctx, selector, 200)
				if err == nil && len(page) > 0 {
					// Читаем курсор продолжения из outbox, а не начинаем обход заново.
					continuation.Kind = appmoderation.TaskAllowTrusted
					continuation.TenantID = "t1"
					return pool.QueryRow(ctx, bulkContinuation).Scan(&continuation.Payload)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if continuation.Kind != "" {
				if err := service.HandleTask(ctx, ports.Task{
					ID:       1,
					Kind:     continuation.Kind,
					TenantID: continuation.TenantID,
					Payload:  continuation.Payload,
				}); err != nil {
					t.Fatal(err)
				}
			}
			for _, item := range cases[:204] {
				details, err := service.CaseDetails(ctx, item.ID)
				if err != nil || details.Case.Status != moderation.Allowed || len(details.History) != 1 {
					t.Fatal(details, err)
				}
			}
			other, err := service.GetCase(ctx, cases[204].ID)
			if err != nil || other.Status != moderation.Open {
				t.Fatal("дело с другой причиной закрыто", other, err)
			}
		})
	}
}

func TestBulkApprovalQueryCount(t *testing.T) {
	for _, mode := range []string{"single", "bulk", "selector"} {
		t.Run(mode, func(t *testing.T) {
			seedPool, _, _, cases := seedBulkApproval(t, 201, 1000)
			trace := &gormPilotQueryTrace{}
			config := seedPool.Config()
			config.ConnConfig.Tracer = trace
			ctx := context.Background()
			pool, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			service := appmoderation.NewService(
				postgres.NewUnitOfWork(pool),
				clock.System{},
				random.IDs{},
				appmoderation.Config{ProbationOffers: 1000},
			)
			cmd := bulkApprovalCommand(cases[:200], "count")
			switch mode {
			case "single":
				for i, item := range cases[:200] {
					_, err = service.Allow(ctx, appmoderation.DecisionCommand{
						CaseID:          item.ID,
						ExpectedVersion: item.ExpectedVersion,
						Note:            cmd.Note,
						Operator:        cmd.Operator,
						IdempotencyKey:  fmt.Sprintf("single-%d", i),
					})
					if err != nil {
						t.Fatal(err)
					}
				}
			case "selector":
				cmd.Cases = nil
				cmd.Selector = &moderation.BulkSelector{
					TenantID: "t1",
					Reasons:  []string{"moderation.probation"},
				}
				fallthrough
			case "bulk":
				result, err := service.BulkAllow(ctx, cmd)
				if err != nil || result.Approved != 200 {
					t.Fatal(result, err)
				}
			}
			trace.mu.Lock()
			defer trace.mu.Unlock()
			sql, transactions := 0, 0
			for _, operation := range trace.operations {
				switch operation {
				case "begin":
					transactions++
				case "commit":
				default:
					sql++
				}
			}
			wantSQL, wantTx := 12, 1
			switch mode {
			case "single":
				wantSQL, wantTx = 2800, 200
			case "selector":
				wantSQL = 13
			}
			t.Logf("SQL=%d, транзакций=%d, с BEGIN/COMMIT=%d", sql, transactions, len(trace.operations))
			if sql != wantSQL || transactions != wantTx {
				t.Fatal(trace.operations)
			}
		})
	}
}

func TestBulkApprovalConcurrentReplay(t *testing.T) {
	_, _, service, cases := seedBulkApproval(t, 200, 1000)
	cmd := bulkApprovalCommand(cases, "same-key")
	var wg sync.WaitGroup
	results := make([]moderation.BulkResult, 2)
	failures := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], failures[i] = service.BulkAllow(context.Background(), cmd)
		}()
	}
	wg.Wait()
	if failures[0] != nil || failures[1] != nil || !reflect.DeepEqual(results[0], results[1]) {
		t.Fatal(results, failures)
	}
	if results[0].Approved != 200 {
		t.Fatal(results)
	}
}

func TestBulkApprovalChangedProducts(t *testing.T) {
	pool, _, service, cases := seedBulkApproval(t, 4, 1000)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, bulkChangedProducts); err != nil {
		t.Fatal(err)
	}
	result, err := service.BulkAllow(ctx, bulkApprovalCommand(cases, "products-changed"))
	if err != nil || result.Approved != 1 || result.Skipped != 3 {
		t.Fatal(result, err)
	}
	for _, skip := range result.Details {
		if skip.Reason != "changed" {
			t.Fatal(skip)
		}
	}
}
