package checkout_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Карта покупки не занимает резерв проверки выбранного ПВЗ другого покупателя.
func TestMapsLeavePointReserve(t *testing.T) {
	for _, kind := range []string{"checkout", "profile"} {
		t.Run(kind, func(t *testing.T) {
			testMapLeavesPointReserve(t, kind)
		})
	}
}

func testMapLeavesPointReserve(t *testing.T, kind string) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	var calls atomic.Int32
	release := make(chan struct{})
	defer close(release)
	e.platform.BeforePickup = func(ctx context.Context, _ ids.TenantID) error {
		if calls.Add(1) > 4 {
			return nil
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for i := range 40 {
		attack, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
		if kind == "checkout" {
			_, _ = e.pickups.CheckoutPoints(
				attack,
				buyerID,
				purchase.ID,
				appdelivery.PickupQuery{
					Carrier: "cdek",
					City:    fmt.Sprintf("Атака %d", i),
					Limit:   100,
				},
			)
		} else {
			_, _ = e.pickups.ProfilePoint(attack, "cdek", "MSK1", fmt.Sprintf("Атака %d", i))
		}
		cancel()
	}
	victim, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	start := time.Now()
	_, pointErr := e.pickups.Point(victim, "buyer-b", "cdek", "MSK1", "Холодный город покупателя Б")
	t.Logf(
		"после 40 запросов карты оформления одного покупателя: Point покупателя Б=%v за "+
			"%s; вызовов магазинов=%d",
		pointErr,
		time.Since(start),
		calls.Load(),
	)
	if pointErr != nil {
		t.Fatalf("карта оформления одного покупателя закрыла выбор ПВЗ другим покупателям процесса")
	}
}
