//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Кодировщик обнуляет отслеживание, ключ со значением null допустим: проверяем, что самих данных там нет.
const trackingInProgress = `SELECT coalesce(order_progress->'Tracking', 'null') <> 'null'
 OR coalesce(order_progress->'tracking', 'null') <> 'null'
 FROM checkout_groups WHERE checkout_id = $1 AND tenant_id = 't1'`

func TestPollTrackingPersistsOutsideOrderProgress(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedBuyer(t, uow, buyerA, "79251234567")
	c := checkout.Restore(checkout.State{
		ID:             buyerB,
		BuyerID:        buyerA,
		IdempotencyKey: "tracking-persistence",
		Status:         checkout.StatusAwaitingPayment,
		Version:        1,
		CreatedAt:      at(0),
		UpdatedAt:      at(0),
		GoodsTotal:     money.FromKopecks(100),
		Total:          money.FromKopecks(100),
		Groups: []checkout.Group{{
			TenantID:           "t1",
			ExternalCheckoutID: string(buyerB) + ":t1",
			Status:             checkout.GroupAwaitingPayment,
			OrderID:            "o1",
			Goods:              money.FromKopecks(100),
			Lines: []checkout.Line{{
				Offer:    ids.OfferID{Tenant: "t1", Product: "p1"},
				Quantity: 1,
				Price:    money.FromKopecks(100),
			}},
		}},
	})
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, c)
	}); err != nil {
		t.Fatal(err)
	}
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		loaded, err := tx.Checkouts().Get(ctx, buyerB)
		if err != nil {
			return err
		}
		claimed, err := loaded.ClaimPoll("t1", "poll", at(1))
		if err != nil {
			return fmt.Errorf("захват опроса: %w", err)
		}
		if !claimed {
			return errors.New("опрос не захвачен")
		}
		_, err = loaded.CompletePoll(
			"t1",
			"poll",
			checkout.SellerOrder{
				OrderID:  "o1",
				Status:   checkout.GroupShipped,
				Tracking: &checkout.Tracking{Carrier: "carrier", Number: nil, URL: "https://example.test/123"},
			},
			at(1).Add(time.Minute),
			at(1),
		)
		if err != nil {
			return err
		}
		return tx.Checkouts().Update(ctx, loaded)
	}); err != nil {
		t.Fatal(err)
	}
	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		loaded, err := tx.Checkouts().Get(ctx, buyerB)
		if err != nil {
			return err
		}
		tracking := loaded.Groups()[0].Tracking
		if tracking == nil || tracking.Number != nil {
			return fmt.Errorf("неверное отслеживание: %+v", tracking)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var duplicated bool
	if err := pool.QueryRow(context.Background(), trackingInProgress, string(buyerB)).Scan(&duplicated); err != nil {
		t.Fatal(err)
	}
	if duplicated {
		t.Fatal("tracking продублирован в order_progress")
	}
}
