package checkout_test

import (
	"context"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

func TestUndeliverableGroupDoesNotBlockOtherShop(t *testing.T) {
	for _, reason := range []string{delivery.ReasonCarrierNotConnected, delivery.ReasonNoRate} {
		t.Run(reason, func(t *testing.T) {
			e := setup(t)
			if reason == delivery.ReasonCarrierNotConnected {
				participant, _ := e.store.Participant("t1")
				participant.Delivery.Carriers = nil
				e.store.SeedParticipant(participant)
			} else {
				e.platform.SetQuote("t1", delivery.Offered{Reason: reason})
			}
			ctx := context.Background()
			state, err := e.service.Create(ctx, buyerID, key)
			if err != nil {
				t.Fatal(err)
			}
			if reason != delivery.ReasonCarrierNotConnected {
				if _, err := e.service.QuoteGroup(ctx, buyerID, state.ID, "t1", "cdek", "MSK1", "Город"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := e.service.QuoteGroup(ctx, buyerID, state.ID, "t2", "cdek", "MSK1", "Город"); err != nil {
				t.Fatal(err)
			}
			if _, err := e.service.Confirm(ctx, buyerID, state.ID); err != nil {
				t.Fatal(err)
			}
			state, err = beginAndExecute(ctx, e, buyerID, state.ID, orderContact)
			if err != nil || e.platform.OrderCount() != 1 || !state.Groups[0].CartReturned ||
				state.Groups[0].CreationResult() != checkout.CreationRejected ||
				state.Groups[1].Status != checkout.GroupAwaitingPayment || state.DeliveryTotal.Kopecks() != 0 {
				t.Fatal(state, err)
			}
			if reason == delivery.ReasonNoRate && len(e.platform.QuoteRequests()) != 3 {
				t.Fatal("отказавшую группу нельзя незаметно включить повторным расчётом")
			}
			if len(e.platform.OrderRequests("t1")) != 0 {
				t.Fatal("магазин без доставки получил заказ")
			}
		})
	}
}

func TestSamePickupQuotesShopsInParallel(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	state, err := e.service.Create(ctx, buyerID, key)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	e.platform.CallHook = func(_ context.Context, operation string) error {
		if operation == "quote" {
			entered <- struct{}{}
			<-release
		}
		return nil
	}
	finished := make(chan error, 1)
	go func() {
		_, err := e.service.QuoteGroups(
			ctx,
			buyerID,
			state.ID,
			[]ids.TenantID{"t1", "t2"},
			"cdek",
			"MSK1",
			"Город",
		)
		finished <- err
	}()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			<-finished
			t.Fatal("магазины должны начать расчёт параллельно")
		}
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	state, err = e.service.Get(ctx, buyerID, state.ID)
	if err != nil || len(e.platform.QuoteRequests()) != 2 {
		t.Fatal(state, err)
	}
	for _, group := range state.Groups {
		if group.Delivery == nil || group.Delivery.PickupPointCode != "MSK1" ||
			group.Quote.Status != delivery.QuoteAvailable || group.Quote.BuyerCost.Kopecks() != 0 {
			t.Fatal(group)
		}
	}
}
