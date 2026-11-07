package checkout_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestPromoOrderSnapshotAndInvalidation(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "expired"}[invalid], func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			e.platform.SetPromoPricing("t1", ports.GroupPricing{
				PromoCode: &cart.PromoCode{Code: "SAVE", Valid: true, Discount: money.FromKopecks(101)},
				Discounts: map[ids.ProductID][]cart.Discount{
					"p1": {{PromotionID: "promo1", Amount: money.FromKopecks(101)}},
				},
			})
			if _, err := e.carts.SetPromo(ctx, buyerID, "t1", "SAVE"); err != nil {
				t.Fatal(err)
			}
			c := confirmedPurchase(t, e)
			if c.Groups[0].Goods.Kopecks() != 397899 {
				t.Fatal(c.Groups[0])
			}
			if invalid {
				e.platform.SetPromoPricing("t1", ports.GroupPricing{
					PromoCode: &cart.PromoCode{Code: "SAVE", Message: expiredPromoMessage()},
				})
			}
			state, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact)
			if invalid {
				if err != nil || e.platform.OrderCount() != 1 ||
					state.Groups[0].LastErrorCode != checkout.ErrPromoInvalid.Key() ||
					!state.Groups[0].CartReturned || state.Groups[1].Status != checkout.GroupAwaitingPayment {
					t.Fatal(state, err)
				}
				basket, err := e.carts.Get(ctx, buyerID)
				if err != nil || len(basket.Lines) != 1 || basket.Lines[0].Offer.Tenant != "t1" ||
					basket.Promos["t1"].Code != "SAVE" || basket.Promos["t1"].Valid ||
					basket.Promos["t1"].Message == nil || *basket.Promos["t1"].Message != "код истёк" {
					t.Fatal(basket, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			requests := e.platform.OrderRequests("t1")
			if len(requests) != 1 || requests[0].PromoCode != "SAVE" || len(requests[0].Items[0].Discounts) != 1 {
				t.Fatal(requests)
			}
			if state.Groups[0].Status != checkout.GroupAwaitingPayment {
				t.Fatal(state.Groups[0])
			}
		})
	}
}

func TestTrackingPollSnapshotAndOwnership(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := confirmedPurchase(t, e)
	state, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	orderID := state.Groups[0].OrderID
	number := "123"
	e.platform.SetTracking(orderID, &checkout.Tracking{
		Carrier: "carrier",
		Number:  &number,
		URL:     "https://example.test/123",
	})
	e.clock.Advance(61 * time.Second)
	state, err = e.service.Refresh(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].Tracking == nil || *state.Groups[0].Tracking.Number != "123" {
		t.Fatal(state, err)
	}
	calls := e.platform.PollCalls("t1")
	*state.Groups[0].Tracking.Number = "changed"
	state, err = e.service.Get(ctx, buyerID, c.ID)
	if err != nil || *state.Groups[0].Tracking.Number != "123" {
		t.Fatal(state, err)
	}
	if e.platform.PollCalls("t1") != calls {
		t.Fatal("чтение покупки вызвало сервер")
	}
	purchases, err := e.service.Purchases(ctx, buyerID, "")
	if err != nil || len(purchases) != 1 || *purchases[0].State().Groups[0].Tracking.Number != "123" {
		t.Fatal(purchases, err)
	}
	if e.platform.PollCalls("t1") != calls {
		t.Fatal("список покупок вызвал сервер")
	}
	e.platform.Fail("t1", errors.New("сервер недоступен"))
	e.clock.Advance(31 * time.Second)
	state, err = e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].Tracking == nil {
		t.Fatal(state, err)
	}
	_, err = e.service.Get(ctx, ids.UUID("other-buyer"), c.ID)
	if !errors.Is(err, checkout.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestPromoOrderRetryAfterCodeExpired(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.platform.SetPromoPricing("t1", ports.GroupPricing{
		PromoCode: &cart.PromoCode{Code: "SAVE", Valid: true, Discount: money.FromKopecks(101)},
		Discounts: map[ids.ProductID][]cart.Discount{"p1": {{PromotionID: "promo1", Amount: money.FromKopecks(101)}}},
	})
	if _, err := e.carts.SetPromo(ctx, buyerID, "t1", "SAVE"); err != nil {
		t.Fatal(err)
	}
	c := confirmedPurchase(t, e)
	crash := errors.New("ответ потерян после создания")
	e.platform.AfterCreate = func() { e.store.FailCommit = crash }
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); !errors.Is(err, crash) {
		t.Fatal(err)
	}
	e.platform.AfterCreate = nil
	e.store.FailCommit = nil
	e.platform.SetPromoPricing("t1", ports.GroupPricing{
		PromoCode: &cart.PromoCode{Code: "SAVE", Message: expiredPromoMessage()},
	})
	e.clock.Advance(2 * time.Minute)
	state, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact)
	if err != nil || state.Groups[0].Status != checkout.GroupAwaitingPayment {
		t.Fatal(state, err)
	}
	if e.platform.OrderCount() != 2 {
		t.Fatal("ожидалось по одному заказу на каждый из двух магазинов")
	}
}

func TestConfirmRepricesDiscountsAndLowerPrice(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.platform.SetPromoPricing("t1", ports.GroupPricing{
		Discounts: map[ids.ProductID][]cart.Discount{"p1": {{PromotionID: "auto", Amount: money.FromKopecks(1000)}}},
	})
	if _, err := e.carts.Verify(ctx, buyerID); err != nil {
		t.Fatal(err)
	}
	c, err := e.service.Create(ctx, buyerID, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Quote(ctx, buyerID, c.ID, "cdek", "MSK1"); err != nil {
		t.Fatal(err)
	}
	e.platform.SetPromoPricing("t1", ports.GroupPricing{
		Discounts: map[ids.ProductID][]cart.Discount{"p1": {{PromotionID: "auto", Amount: money.FromKopecks(10)}}},
	})
	e.platform.SetItem("t1", ports.ItemCheck{
		ProductID: "p1",
		Exists:    true,
		Available: 3,
		Price:     money.FromKopecks(100),
	})
	e.verifier.Err = errors.New("корзина недоступна")
	state, err := e.service.Confirm(ctx, buyerID, c.ID)
	if e.verifier.Calls != 1 {
		t.Fatal(e.verifier.Calls)
	}
	e.verifier.Err = nil
	if !errors.Is(err, checkout.ErrChanged) || state.Groups[0].Goods.Kopecks() != 190 {
		t.Fatal(state, err)
	}
	saved, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || saved.Groups[0].Goods.Kopecks() != 190 {
		t.Fatal(saved, err)
	}
	state, err = e.service.Confirm(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusConfirmed {
		t.Fatal(state, err)
	}
	state, err = e.service.CreateOrders(ctx, buyerID, c.ID, orderContact)
	if err != nil || state.Groups[0].Status != checkout.GroupAwaitingPayment {
		t.Fatal(state, err)
	}
	requests := e.platform.OrderRequests("t1")
	if requests[0].PromoCode != "" || requests[0].Items[0].Discounts == nil ||
		len(requests[0].Items[0].Discounts) != 1 {
		t.Fatal(requests)
	}
}

func TestCreateChangedDiscountRefreshesCart(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.platform.SetPromoPricing("t1", ports.GroupPricing{
		Discounts: map[ids.ProductID][]cart.Discount{"p1": {{PromotionID: "auto", Amount: money.FromKopecks(1000)}}},
	})
	if _, err := e.service.Create(ctx, buyerID, key); !errors.Is(err, checkout.ErrChanged) {
		t.Fatal(err)
	}
	c, err := e.service.Create(ctx, buyerID, key)
	if err != nil || c.Groups[0].Goods.Kopecks() != 397000 {
		t.Fatal(c, err)
	}
}

func TestRejectedPromoNotCopiedToCheckout(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.carts.SetPromo(ctx, buyerID, "t1", "BAD"); err != nil {
		t.Fatal(err)
	}
	c, err := e.service.Create(ctx, buyerID, key)
	if err != nil || c.Groups[0].PromoCode != "" {
		t.Fatal(c, err)
	}
}

func TestConfirmRejectsExpiredPromo(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.platform.SetPromoPricing("t1", ports.GroupPricing{PromoCode: &cart.PromoCode{Code: "SAVE", Valid: true}})
	if _, err := e.carts.SetPromo(ctx, buyerID, "t1", "SAVE"); err != nil {
		t.Fatal(err)
	}
	c, err := e.service.Create(ctx, buyerID, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Quote(ctx, buyerID, c.ID, "cdek", "MSK1"); err != nil {
		t.Fatal(err)
	}
	e.platform.SetPromoPricing("t1", ports.GroupPricing{
		PromoCode: &cart.PromoCode{Code: "SAVE", Message: expiredPromoMessage()},
	})
	e.verifier.Err = errors.New("корзина недоступна")
	_, err = e.service.Confirm(ctx, buyerID, c.ID)
	if !errors.Is(err, checkout.ErrPromoInvalid) || !strings.Contains(err.Error(), "код истёк") {
		t.Fatal(err)
	}
}

func TestExpiredPromoRefreshesCartAndAllowsNewCheckout(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		t.Run(map[bool]string{false: "Create", true: "Confirm"}[confirm], func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			e.platform.SetPromoPricing("t1", ports.GroupPricing{PromoCode: &cart.PromoCode{Code: "SAVE", Valid: true}})
			if _, err := e.carts.SetPromo(ctx, buyerID, "t1", "SAVE"); err != nil {
				t.Fatal(err)
			}
			var purchase checkout.State
			if confirm {
				var err error
				purchase, err = e.service.Create(ctx, buyerID, key)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e.service.Quote(ctx, buyerID, purchase.ID, "cdek", "MSK1"); err != nil {
					t.Fatal(err)
				}
			}
			message := "код истёк"
			e.platform.SetPromoPricing("t1", ports.GroupPricing{
				PromoCode: &cart.PromoCode{Code: "SAVE", Message: &message},
			})
			var err error
			if confirm {
				_, err = e.service.Confirm(ctx, buyerID, purchase.ID)
			} else {
				_, err = e.service.Create(ctx, buyerID, key)
			}
			if !errors.Is(err, checkout.ErrPromoInvalid) {
				t.Fatal(err)
			}
			basket, err := e.carts.Get(ctx, buyerID)
			if err != nil || basket.Promos["t1"].Valid || basket.Promos["t1"].Message == nil {
				t.Fatal(basket, err)
			}
			created, err := e.service.Create(ctx, buyerID, "new-key-after-expiry")
			if err != nil || created.Groups[0].PromoCode != "" {
				t.Fatal(created, err)
			}
		})
	}
}

func TestReturnedGroupKeepsUncheckedPromo(t *testing.T) {
	for _, code := range []string{"OUT_OF_STOCK", "DELIVERY_UNAVAILABLE", "DELIVERY_CHANGED"} {
		t.Run(code, func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			e.platform.SetPromoPricing("t1", ports.GroupPricing{PromoCode: &cart.PromoCode{Code: "SAVE", Valid: true}})
			if _, err := e.carts.SetPromo(ctx, buyerID, "t1", "SAVE"); err != nil {
				t.Fatal(err)
			}
			c := confirmedPurchase(t, e)
			e.platform.FailCreate("t1", &ports.OrderRefusal{Code: code})
			if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
				t.Fatal(err)
			}
			basket, err := e.carts.Get(ctx, buyerID)
			promo := basket.Promos["t1"]
			if err != nil || promo.Code != "SAVE" || promo.Valid || promo.Message != nil {
				t.Fatal(basket, err)
			}
			created, err := e.service.Create(ctx, buyerID, "new-key-after-return")
			if err != nil || created.Groups[0].PromoCode != "SAVE" {
				t.Fatal(created, err)
			}
		})
	}
}

func TestConsumedCartRechecksUncheckedPromo(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.platform.SetPromoPricing("t1", ports.GroupPricing{PromoCode: &cart.PromoCode{Code: "SAVE", Valid: true}})
	if _, err := e.carts.SetPromo(ctx, buyerID, "t1", "SAVE"); err != nil {
		t.Fatal(err)
	}
	err := e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		basket, err := tx.Carts().ByBuyer(ctx, buyerID)
		if err != nil {
			return err
		}
		basket.Consume(p1, 1, e.clock.Now(), 24*time.Hour)
		return tx.Carts().Save(ctx, basket)
	})
	if err != nil {
		t.Fatal(err)
	}
	basket, err := e.carts.Get(ctx, buyerID)
	if err != nil || basket.Promos["t1"].Valid || basket.Promos["t1"].Message != nil {
		t.Fatal(basket, err)
	}
	created, err := e.service.Create(ctx, buyerID, key)
	if err != nil || created.Groups[0].PromoCode != "SAVE" {
		t.Fatal(created, err)
	}
}

func expiredPromoMessage() *string {
	message := "код истёк"
	return &message
}

func TestReturnedGroupKeepsNewCartPromo(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.platform.SetPromoPricing("t1", ports.GroupPricing{PromoCode: &cart.PromoCode{Code: "OLD", Valid: true}})
	if _, err := e.carts.SetPromo(ctx, buyerID, "t1", "OLD"); err != nil {
		t.Fatal(err)
	}
	purchase := confirmedPurchase(t, e)
	e.platform.FailCreate("t1", errors.New("нет ответа"))
	if _, err := e.service.CreateOrders(ctx, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.platform.SetPromoPricing("t1", ports.GroupPricing{PromoCode: &cart.PromoCode{Code: "NEW", Valid: true}})
	if _, err := e.carts.Add(ctx, buyerID, p1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.carts.SetPromo(ctx, buyerID, "t1", "NEW"); err != nil {
		t.Fatal(err)
	}
	e.platform.FailCreate("t1", checkout.ErrPromoInvalid.WithMessage("старый код истёк"))
	e.clock.Advance(2 * time.Minute)
	if _, err := e.service.CreateOrders(ctx, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	basket, err := e.carts.Get(ctx, buyerID)
	if err != nil || basket.Promos["t1"].Code != "NEW" || basket.Promos["t1"].Message != nil {
		t.Fatal(basket, err)
	}
}

func TestCreateUnavailableRefreshesCart(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.platform.SetItem("t1", ports.ItemCheck{ProductID: "p1"})
	if _, err := e.service.Create(ctx, buyerID, key); !errors.Is(err, checkout.ErrOfferUnavailable) {
		t.Fatal(err)
	}
	basket, err := e.carts.Get(ctx, buyerID)
	if err != nil || basket.Lines[0].Issue != cart.IssueUnavailable {
		t.Fatal(basket, err)
	}
}

func TestReturnedGroupMarksSameCartPromoRejected(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.platform.SetPromoPricing("t1", ports.GroupPricing{PromoCode: &cart.PromoCode{Code: "OLD", Valid: true}})
	if _, err := e.carts.SetPromo(ctx, buyerID, "t1", "OLD"); err != nil {
		t.Fatal(err)
	}
	purchase := confirmedPurchase(t, e)
	e.platform.FailCreate("t1", errors.New("нет ответа"))
	if _, err := e.service.CreateOrders(ctx, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.platform.SetPromoPricing("t1", ports.GroupPricing{PromoCode: &cart.PromoCode{Code: "OLD", Valid: true}})
	if _, err := e.carts.Add(ctx, buyerID, p1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.carts.SetPromo(ctx, buyerID, "t1", "OLD"); err != nil {
		t.Fatal(err)
	}
	e.platform.FailCreate("t1", checkout.ErrPromoInvalid.WithMessage("старый код истёк"))
	e.clock.Advance(2 * time.Minute)
	if _, err := e.service.CreateOrders(ctx, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	basket, err := e.carts.Get(ctx, buyerID)
	if err != nil || basket.Promos["t1"].Code != "OLD" || basket.Promos["t1"].Message == nil ||
		*basket.Promos["t1"].Message != "старый код истёк" || basket.Promos["t1"].Valid {
		t.Fatal(basket, err)
	}
}

func TestCreateKeepsPrimaryErrorWhenCartVerifyFails(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed", true: "unavailable"}[unavailable], func(t *testing.T) {
			e := setup(t)
			e.verifier.Err = errors.New("корзина недоступна")
			want := checkout.ErrChanged
			if unavailable {
				e.platform.SetItem("t1", ports.ItemCheck{ProductID: "p1"})
				want = checkout.ErrOfferUnavailable
			} else {
				e.platform.SetPromoPricing("t1", ports.GroupPricing{
					Discounts: map[ids.ProductID][]cart.Discount{
						"p1": {{PromotionID: "auto", Amount: money.FromKopecks(1000)}},
					},
				})
			}
			if _, err := e.service.Create(context.Background(), buyerID, key); !errors.Is(err, want) {
				t.Fatal(err)
			}
			if e.verifier.Calls != 1 {
				t.Fatal(e.verifier.Calls)
			}
		})
	}
}
