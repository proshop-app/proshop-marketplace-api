//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestDispatchReviewPersistsGenerationButNotLiveModerationHold(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	seedRecommendationProducts(t, uow, "t1", 1)
	seedBuyer(t, uow, buyerA, "79251234567")
	ctx := context.Background()
	c, err := checkout.New(
		"10000000-0000-4000-8000-000000000001",
		buyerA,
		"dispatch-review-persistence",
		[]checkout.Line{{
			Offer:    ids.OfferID{Tenant: "t1", Product: "p1"},
			Quantity: 1,
			Price:    money.FromKopecks(100),
		}},
		at(0),
	)
	if err != nil {
		t.Fatal(err)
	}
	state := c.State()
	state.Status = checkout.StatusDraft
	group := &state.Groups[0]
	group.Status = checkout.GroupDeliveryRequoted
	group.Reason = checkout.DispatchOriginChanged
	group.DispatchReviewRevision = 7
	group.PaymentHold = checkout.HoldModerationPending
	group.Quote = &delivery.Quote{Status: delivery.QuoteAvailable, MinDays: 7, MaxDays: 9, ExpiresAt: at(60)}
	group.Delivery = &checkout.Selection{Carrier: "cdek", PickupPointCode: "P1", City: "Город"}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, checkout.Restore(state))
	}); err != nil {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		restored, err := tx.Checkouts().Get(ctx, c.ID())
		if err != nil {
			return err
		}
		group := restored.Groups()[0]
		if group.Status != checkout.GroupDeliveryRequoted || group.DispatchReviewRevision != 7 ||
			group.Quote.MinDays != 7 || group.DeliveryPrice.Kopecks() != 0 || group.PaymentHold != "" {
			t.Fatal(group)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
