package checkout_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

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
	const blocked = 4
	started := make(chan struct{}, blocked)
	release := make(chan struct{})
	var attacks sync.WaitGroup
	defer func() {
		close(release)
		attacks.Wait()
	}()
	e.platform.BeforePickup = func(_ context.Context, _ ids.TenantID) error {
		if calls.Add(1) <= blocked {
			started <- struct{}{}
			<-release
		}
		return nil
	}
	for i := range 40 {
		attacks.Add(1)
		go func() {
			defer attacks.Done()
			if kind == "checkout" {
				_, _ = e.pickups.CheckoutPoints(
					ctx,
					e.service,
					buyerID,
					purchase.ID,
					appdelivery.PickupQuery{
						Carrier: "cdek",
						City:    fmt.Sprintf("Атака %d", i),
						Limit:   100,
					},
				)
				return
			}
			_, _ = e.pickups.ProfilePoint(ctx, "cdek", "MSK1", fmt.Sprintf("Атака %d", i))
		}()
	}
	for range blocked {
		<-started
	}
	_, pointErr := e.pickups.Point(ctx, "buyer-b", "cdek", "MSK1", "Холодный город покупателя Б")
	if pointErr != nil {
		t.Fatalf("карта оформления одного покупателя закрыла выбор ПВЗ другим покупателям процесса")
	}
}
