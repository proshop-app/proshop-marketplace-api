//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const (
	seedCheckoutWorkProgress = `UPDATE checkout_groups SET order_progress = $1`
	selectCheckoutWork       = `SELECT next_work_at FROM checkout_groups LIMIT 1`
)

func TestCheckoutWorkBackfillMatchesDomain(t *testing.T) {
	pool := testPool(t)
	seedCheckoutList(t, postgres.NewUnitOfWork(pool))
	ctx := context.Background()
	cases := []checkout.Group{
		{Status: checkout.GroupCreating},
		{Status: checkout.GroupSellerUnreachable, CreateExhausted: true},
		{Status: checkout.GroupSellerUnreachable, CreateExhausted: true, NextReturnAt: at(10)},
		{Status: checkout.GroupCancelled, CartReturned: true},
		{Status: checkout.GroupAwaitingPayment, OrderID: "order", NextPollAt: at(20)},
		{
			Status:                 checkout.GroupUnconfirmed,
			Abandoned:              true,
			ReconciliationRequired: true,
			OrderID:                "order",
			NextReturnAt:           at(10),
			NextPollAt:             at(20),
		},
		{
			Status:          checkout.GroupSellerUnreachable,
			LastOrderStatus: checkout.GroupCancelled,
			OrderID:         "order",
			NextPollAt:      at(20),
			NextReturnAt:    at(10),
		},
	}
	for _, g := range cases {
		if err := postgres.MigrateDownTo(ctx, pool, 50); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(g)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, seedCheckoutWorkProgress, raw); err != nil {
			t.Fatal(err)
		}
		if err := migrateUpTo(ctx, pool, 51); err != nil {
			t.Fatal(err)
		}
		var next *time.Time
		if err := pool.QueryRow(ctx, selectCheckoutWork).Scan(&next); err != nil {
			t.Fatal(err)
		}
		expected, found := g.NextWorkAt()
		if (next != nil) != found || (found && !next.Equal(expected)) {
			t.Fatal(g, next, expected, found)
		}
	}
}
