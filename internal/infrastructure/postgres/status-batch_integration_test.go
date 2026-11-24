//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

type statusBatchPublisher struct {
	fake.Publisher
	single      atomic.Int64
	batches     atomic.Int64
	failProduct string
}

func (p *statusBatchPublisher) Publish(ctx context.Context, message ports.Message) error {
	p.single.Add(1)
	return p.Publisher.Publish(ctx, message)
}

func (p *statusBatchPublisher) PublishBatch(ctx context.Context, messages []ports.Message) []error {
	p.batches.Add(1)
	failures := make([]error, len(messages))
	for n, message := range messages {
		var event moderationstatus.OfferEvent
		if err := json.Unmarshal(message.Value, &event); err != nil {
			failures[n] = err
		} else if string(event.ProductID) == p.failProduct {
			failures[n] = errors.New("отказ одного сообщения")
		} else {
			failures[n] = p.Publisher.Publish(ctx, message)
		}
	}
	return failures
}

func TestOfferStatusBatchSQLAndPartialDelivery(t *testing.T) {
	checks := []struct {
		name        string
		partial     bool
		multitenant bool
		unpublished bool
	}{
		{name: "200"},
		{name: "partial", partial: true},
		{name: "mixed-tenants", multitenant: true},
		{name: "legacy-unpublished", unpublished: true},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			partial := check.partial
			pool, _, approval, cases := seedBulkApproval(t, 200, 1000)
			ctx := context.Background()
			if _, err := approval.BulkAllow(ctx, bulkApprovalCommand(cases, "status")); err != nil {
				t.Fatal(err)
			}
			if check.unpublished {
				if _, err := pool.Exec(ctx, `UPDATE catalog_products SET card = card - 'publication'
                    WHERE product_id = 'p000'`); err != nil {
					t.Fatal(err)
				}
			}
			if check.multitenant {
				for _, table := range []string{"catalog_products", "moderation_verdicts", "moderation_cases"} {
					query := "UPDATE " + table + " SET tenant_id = 't2' WHERE product_id = 'p000'"
					if _, err := pool.Exec(ctx, query); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := pool.Exec(
					ctx,
					`UPDATE outbox SET tenant_id = 't2',
                    payload = jsonb_set(payload, '{tenantId}', '"t2"')
                    WHERE kind = $1 AND payload->>'productId' = 'p000'`,
					moderationstatus.TaskOffer,
				); err != nil {
					t.Fatal(err)
				}
			}
			config := pool.Config()
			trace := &gormPilotQueryTrace{}
			config.ConnConfig.Tracer = trace
			traced, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer traced.Close()
			publisher := &statusBatchPublisher{}
			if partial {
				publisher.failProduct = "p003"
				if _, err := pool.Exec(ctx, `UPDATE catalog_products
                    SET card = jsonb_set(card, '{publication}', '"bad"') WHERE product_id = 'p001'`); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(
					ctx,
					`UPDATE outbox SET payload = '{}' WHERE kind = $1
                    AND payload->>'productId' = 'p002'`,
					moderationstatus.TaskOffer,
				); err != nil {
					t.Fatal(err)
				}
			}
			statuses := moderationstatus.NewService(
				postgres.NewUnitOfWork(traced),
				clock.System{},
				publisher,
				"offer-status",
				"participant-status",
			)
			dispatcher := messaging.NewDispatcher(
				postgres.NewTaskQueue(postgres.Single(traced)),
				clock.System{},
				messaging.DispatcherConfig{Batch: 200, PerTenant: 200},
				nil,
			)
			dispatcher.RegisterBatch(moderationstatus.TaskOffer, statuses.HandleOfferBatch)
			// Исключаем остальные виды из этого измерения доставки статусов.
			if _, err := pool.Exec(ctx, "DELETE FROM outbox WHERE kind <> $1", moderationstatus.TaskOffer); err != nil {
				t.Fatal(err)
			}
			outcomes, err := dispatcher.RunOnce(ctx)
			if err != nil || len(outcomes) != 200 {
				t.Fatal(len(outcomes), err)
			}
			sql, transactions := 0, 0
			for _, operation := range trace.operations {
				if operation == "begin" {
					transactions++
				} else if operation != "commit" && operation != "rollback" {
					sql++
				}
			}
			failed := 0
			for _, outcome := range outcomes {
				if outcome.Err != nil {
					failed++
				}
			}
			t.Logf(
				"SQL=%d tx=%d single-publish=%d batch-publish=%d sent=%d failed=%d",
				sql,
				transactions,
				publisher.single.Load(),
				publisher.batches.Load(),
				len(publisher.Messages()),
				failed,
			)
			wantFailed := 0
			if partial {
				wantFailed = 3
			}
			for _, message := range publisher.Messages() {
				var event moderationstatus.OfferEvent
				if err := json.Unmarshal(message.Value, &event); err != nil {
					t.Fatal(err)
				}
				wantStatus := "published"
				if check.unpublished && event.ProductID == "p000" {
					wantStatus = "hidden"
				}
				if event.Status != wantStatus || event.Decision == nil || event.Decision.Note != "Проверено пачкой" {
					t.Fatal("потеряна обратная связь решения", event)
				}
				if message.Headers["x-tenant"] != string(event.TenantID) ||
					(check.multitenant && event.ProductID == "p000" && event.TenantID != "t2") {
					t.Fatal("перепутаны магазины", event)
				}
			}
			var done int
			query := "SELECT count(*) FROM outbox WHERE done_at IS NOT NULL"
			if err := pool.QueryRow(ctx, query).Scan(&done); err != nil {
				t.Fatal(err)
			}
			if done != 200-wantFailed {
				t.Fatal("повторяются успешные соседи", done)
			}
			if sql > 5 || transactions != 2 || publisher.batches.Load() != 1 ||
				len(publisher.Messages()) != 200-wantFailed || failed != wantFailed {
				t.Fatal("доставка должна быть пакетной, отказы — по элементам")
			}
		})
	}
}
