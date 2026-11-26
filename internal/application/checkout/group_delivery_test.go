package checkout_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
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
			if _, err := e.service.Quote(ctx, buyerID, state.ID, "cdek", "MSK1", "Город"); err != nil {
				t.Fatal(err)
			}
			if _, err := e.service.Confirm(ctx, buyerID, state.ID); err != nil {
				t.Fatal(err)
			}
			state, err = beginAndExecute(ctx, e, buyerID, state.ID, orderContact)
			if err != nil || e.platform.OrderCount() != 1 || !state.DeliveryTotal.IsZero() {
				t.Fatal(state, err)
			}
			if reason == delivery.ReasonCarrierNotConnected {
				if len(state.Groups) != 1 || state.Groups[0].TenantID != "t2" {
					t.Fatal("магазин без доставки не должен входить в покупку", state)
				}
				basket, _ := e.carts.Get(ctx, buyerID)
				if len(basket.Lines) != 1 || basket.Lines[0].Offer != p1 {
					t.Fatal("исключённый магазин остаётся в корзине", basket)
				}
			} else if len(state.Groups) != 2 || !state.Groups[0].CartReturned ||
				state.Groups[0].CreationResult() != checkout.CreationRejected {
				t.Fatal(state)
			}
			if len(e.platform.OrderRequests("t1")) != 0 {
				t.Fatal("магазин без доставки получил заказ")
			}
		})
	}
}

func TestSamePickupQuotesShopsInParallel(t *testing.T) {
	e := setup(t)
	e.platform.SetQuote("t1", delivery.Offered{
		Available: true,
		Cost:      money.FromKopecks(19000),
		MinDays:   2,
		MaxDays:   3,
	})
	e.platform.SetQuote("t2", delivery.Offered{
		Available: true,
		Cost:      money.FromKopecks(29000),
		MinDays:   5,
		MaxDays:   8,
	})
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
		_, err := e.service.Quote(
			ctx,
			buyerID,
			state.ID,
			"cdek",
			"MSK1",
			"Город",
		)
		finished <- err
	}()
	for range 2 {
		<-entered
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
		if state.Delivery == nil || state.Delivery.PickupPointCode != "MSK1" ||
			group.Quote.Status != delivery.QuoteAvailable || !group.DeliveryPrice.IsZero() {
			t.Fatal(group)
		}
	}
	if state.Groups[0].Quote.MinDays != 2 || state.Groups[1].Quote.MaxDays != 8 ||
		state.Groups[0].Quote.Cost.Kopecks() != 19000 || state.Groups[1].Quote.Cost.Kopecks() != 29000 {
		t.Fatal(state.Groups)
	}
	e.platform.CallHook = nil
	confirmed, err := e.service.Confirm(ctx, buyerID, state.ID)
	if err != nil || confirmed.Status != checkout.StatusConfirmed || !confirmed.DeliveryTotal.IsZero() ||
		confirmed.Total != confirmed.GoodsTotal {
		t.Fatal(confirmed, err)
	}

}
