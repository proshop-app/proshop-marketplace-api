//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	seedReconcileProgress = `
		UPDATE checkout_groups
		SET order_progress = order_progress || $1::jsonb
		WHERE checkout_id = $2 AND tenant_id = $3`
	seedAllReconcileProgress = `UPDATE checkout_groups SET order_progress = $1, next_work_at = $2`
	selectReconcileColumns   = `SELECT reconcile_until, next_work_at FROM checkout_groups LIMIT 1`
	selectReconcileUntil     = `
		SELECT reconcile_until
		FROM checkout_groups
		WHERE checkout_id = $1 AND tenant_id = $2`
)

func TestOrderReconciliationBackfillMatchesDomain(t *testing.T) {
	pool := testPool(t)
	seedCheckoutList(t, postgres.NewUnitOfWork(pool))
	ctx := context.Background()
	cases := []checkout.Group{
		{Status: checkout.GroupAwaitingPayment, OrderID: "order", NextPollAt: at(20)},
		{Status: checkout.GroupPaid, OrderID: "order", NextPollAt: at(20)},
		{Status: checkout.GroupPaid, OrderID: "order", NextPollAt: at(20), AwaitingSince: at(5)},
		{Status: checkout.GroupShipped, OrderID: "order", NextPollAt: at(20)},
		{Status: checkout.GroupDelivered, OrderID: "order", NextPollAt: at(20)},
		{Status: checkout.GroupReturned, OrderID: "order", NextPollAt: at(20)},
		{
			Status:          checkout.GroupSellerUnreachable,
			LastOrderStatus: checkout.GroupPaid,
			OrderID:         "order",
			NextPollAt:      at(20),
		},
		{
			Status:                 checkout.GroupUnconfirmed,
			LastOrderStatus:        checkout.GroupPaid,
			Abandoned:              true,
			CancellationBlocked:    true,
			ReconciliationRequired: true,
			OrderID:                "order",
			NextPollAt:             at(20),
		},
	}
	for _, g := range cases {
		if err := postgres.MigrateDownTo(ctx, pool, 66); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(g)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, seedAllReconcileProgress, raw, at(20)); err != nil {
			t.Fatal(err)
		}
		if err := migrateUpTo(ctx, pool, 67); err != nil {
			t.Fatal(err)
		}
		var until, work *time.Time
		if err := pool.QueryRow(ctx, selectReconcileColumns).Scan(&until, &work); err != nil {
			t.Fatal(err)
		}
		expected := g.ReconcileUntil()
		if (until != nil) != !expected.IsZero() || (until != nil && !until.Equal(expected)) {
			t.Fatalf("срок сверки %s: %v, домен %v", g.Status, until, expected)
		}
		if _, found := g.NextWorkAt(); (work != nil) != found {
			t.Fatalf("фоновая работа %s: %v, домен %v", g.Status, work, found)
		}
	}
}

func TestReconcileDueWalksShopsWithoutPurchaseSnapshots(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	purchases := seedCheckoutList(t, uow)
	ctx := context.Background()
	now := at(0)
	seed := func(index int, tenant ids.TenantID, g checkout.Group) {
		t.Helper()
		g.OrderID = fmt.Sprintf("o-%d-%s", index, tenant)
		// Оплаченный заказ заводится полями группы; целевой статус ставит домен, срок сверки — репозиторий.
		raw, err := json.Marshal(map[string]any{
			"Status":          checkout.GroupPaid,
			"LastOrderStatus": checkout.GroupPaid,
			"OrderID":         g.OrderID,
			"OrderRevision":   g.OrderRevision,
		})
		if err != nil {
			t.Fatal(err)
		}
		id := string(purchases[index].ID())
		if _, err := pool.Exec(ctx, seedReconcileProgress, raw, id, string(tenant)); err != nil {
			t.Fatal(err)
		}
		err = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
			loaded, err := tx.Checkouts().Get(ctx, purchases[index].ID())
			if err != nil {
				return err
			}
			order := checkout.SellerOrder{OrderID: g.OrderID, Status: g.Status, Revision: g.OrderRevision + 1}
			if _, err := loaded.ApplyOrderState(tenant, order, now); err != nil {
				return err
			}
			return tx.Checkouts().Update(ctx, loaded)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	seed(0, "t1", checkout.Group{Status: checkout.GroupPaid})
	seed(1, "t1", checkout.Group{Status: checkout.GroupAssembled, OrderRevision: 4})
	seed(1, "t2", checkout.Group{Status: checkout.GroupShipped})
	seed(2, "t2", checkout.Group{Status: checkout.GroupReturned})
	seed(3, "t1", checkout.Group{Status: checkout.GroupDelivered})

	var entries []checkout.ReconcileEntry
	page := func(after checkout.ReconcileCursor, at time.Time, limit int) {
		t.Helper()
		err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
			var err error
			entries, err = tx.Checkouts().ReconcileDue(ctx, after, at, limit)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	page(checkout.ReconcileCursor{}, now, 2)
	if len(entries) != 2 || entries[0].TenantID != "t1" || entries[1].TenantID != "t1" ||
		entries[1].Status != checkout.GroupAssembled || entries[1].Revision != 5 ||
		entries[1].OrderID != "o-1-t1" || entries[1].CheckoutID != purchases[1].ID() {
		t.Fatalf("заказы одного магазина идут подряд: %+v", entries)
	}
	last := entries[1]
	page(checkout.ReconcileCursor{TenantID: last.TenantID, CheckoutID: last.CheckoutID}, now, 2)
	if len(entries) != 1 || entries[0].TenantID != "t2" || entries[0].Status != checkout.GroupShipped {
		t.Fatalf("продолжение обхода без возвращённого и полученного: %+v", entries)
	}

	later := now.Add(checkout.ReconcileInFlightFor + time.Minute)
	page(checkout.ReconcileCursor{}, later, 10)
	if len(entries) != 0 {
		t.Fatalf("заказ без движения после срока в сверку не входит: %+v", entries)
	}
	err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().ExpireReconciliation(ctx, later)
	})
	if err != nil {
		t.Fatal(err)
	}
	var until *time.Time
	stuck := string(purchases[0].ID())
	if err := pool.QueryRow(ctx, selectReconcileUntil, stuck, "t1").Scan(&until); err != nil || until != nil {
		t.Fatalf("истёкший срок снят из индекса сверки: %v %v", until, err)
	}
}
