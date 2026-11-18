package checkout_test

import (
	"context"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

func TestTemporaryProductReviewPreservesOrderUntilPaymentDeadline(t *testing.T) {
	for _, condition := range []string{
		"pending",
		"unpublished",
		"hidden",
		"unpublished_unreviewed",
		"pending_price_profile",
	} {
		t.Run(
			condition,
			func(t *testing.T) {
				e := setup(t)
				ctx := context.Background()
				purchase := confirmedPurchase(t, e)
				before, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
				if err != nil {
					t.Fatal(err)
				}
				product, _ := e.store.Product("t1", "p1")
				original := product
				switch condition {
				case "pending":
					e.store.SeedVerdict(
						moderation.VerdictState{
							TenantID:  "t1",
							ProductID: "p1",
							Status:    moderation.Pending,
						},
					)
				case "pending_price_profile":
					e.store.SeedVerdict(
						moderation.VerdictState{
							TenantID:  "t1",
							ProductID: "p1",
							Status:    moderation.Pending,
						},
					)
					product.PriceProfiles = nil
				case "unpublished":
					product.Card.Publication.Published = false
				case "unpublished_unreviewed":
					product.Card.Publication.Published = false
					e.store.RemoveVerdict("t1", "p1")
				case "hidden":
					product.Card.Visible = false
				}
				e.store.SeedProduct(product)
				paused, err := e.service.Pay(ctx, buyerID, purchase.ID)
				if err != nil || paused.Status != before.Status ||
					paused.Total != before.Total || paused.Groups[0].PaymentHold == "" || paused.Groups[0].Excluded() {
					t.Fatal(paused, err)
				}
				if err := e.service.ConfirmPayment(ctx, buyerID, purchase.ID); err != nil {
					t.Fatal(err)
				}
				if len(e.platform.StubPaymentCalls()) != 1 || len(e.platform.CancellationRequests("t1")) != 0 {
					t.Fatal("ожидание сняло заказ или взяло деньги")
				}
				partial, err := e.service.Get(ctx, buyerID, purchase.ID)
				if err != nil ||
					partial.Status != checkout.StatusPartiallyPaid ||
					!partial.Groups[1].Paid() ||
					partial.Groups[0].Paid() {
					t.Fatal(partial, err)
				}
				e.store.SeedProduct(original)
				e.store.SeedVerdict(moderation.VerdictState{
					TenantID:  "t1",
					ProductID: "p1",
					Status:    moderation.Allowed,
				})
				resumed, err := e.service.Get(ctx, buyerID, purchase.ID)
				if err != nil || resumed.Groups[0].PaymentHold != "" || resumed.Status != checkout.StatusPartiallyPaid {
					t.Fatal(resumed, err)
				}
				if err := e.service.ConfirmPayment(ctx, buyerID, purchase.ID); err != nil {
					t.Fatal(err)
				}
				if len(e.platform.OrderRequests("t1")) != 1 || resumed.Groups[0].OrderID != before.Groups[0].OrderID {
					t.Fatal("заказ пересоздан после возвращения товара")
				}
			},
		)
	}
}

func TestPendingReviewDoesNotExtendPaymentDeadline(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	before, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	e.store.SeedVerdict(
		moderation.VerdictState{
			TenantID:  "t1",
			ProductID: "p1",
			Status:    moderation.Pending,
		},
	)
	if err := e.service.ConfirmPayment(ctx, buyerID, purchase.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(before.Groups[0].ExpiresAt.Sub(e.clock.Now()))
	_, err = e.service.Pay(ctx, buyerID, purchase.ID)
	if !errors.Is(err, checkout.ErrPaymentExpired) {
		t.Fatal(err)
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, purchase.ID); !errors.Is(err, checkout.ErrPaymentExpired) {
		t.Fatal(err)
	}
	if len(e.platform.StubPaymentCalls()) != 1 || len(e.platform.CancellationRequests("t1")) != 0 {
		t.Fatal("проверка срока сделала платёж или отменила заказ вместо обычного истечения магазина")
	}
	e.platform.SetOrderStatus(before.Groups[0].OrderID, checkout.GroupCancelled, "payment_expired")
	if err := e.service.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := e.service.ReturnCancelled(ctx, buyerID, purchase.ID); err != nil {
		t.Fatal(err)
	}
	expired, err := e.service.Get(ctx, buyerID, purchase.ID)
	if err != nil ||
		expired.Status != checkout.StatusPaid ||
		!expired.Groups[1].Paid() ||
		expired.Total != before.Groups[1].Goods ||
		expired.Groups[0].Status != checkout.GroupCancelled ||
		!expired.Groups[0].CartReturned || expired.Groups[0].ExpiresAt != before.Groups[0].ExpiresAt {
		t.Fatal(expired, err)
	}
}

func TestRejectedReviewRequiresConfirmationOfRemainingPurchase(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	before, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	e.store.SeedVerdict(moderation.VerdictState{TenantID: "t1", ProductID: "p1", Status: moderation.Rejected})
	after, err := e.service.Pay(ctx, buyerID, purchase.ID)
	if !errors.Is(err, checkout.ErrDeliveryChanged) || after.Status != checkout.StatusDraft ||
		!after.Groups[0].Excluded() || after.Groups[1].OrderID != before.Groups[1].OrderID {
		t.Fatal(after, err)
	}
	if len(e.platform.StubPaymentCalls()) != 0 {
		t.Fatal("нет нового согласия покупателя")
	}
}
