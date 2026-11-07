package cart_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestPromoRevalidationAndRemoval(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.service.Add(ctx, buyerID, p1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Add(ctx, buyerID, p2, 1); err != nil {
		t.Fatal(err)
	}
	e.platform.SetPromoPricing("t1", ports.GroupPricing{
		PromoCode: &cart.PromoCode{Code: "SAVE", Valid: true, Discount: money.FromKopecks(101)},
		Discounts: map[ids.ProductID][]cart.Discount{"p1": {{PromotionID: "promo1", Amount: money.FromKopecks(101)}}},
	})
	state, err := e.service.SetPromo(ctx, buyerID, "t1", "SAVE")
	if err != nil {
		t.Fatal(err)
	}
	total, err := cart.Restore(state).Total()
	if err != nil || total.Kopecks() != 432899 || state.Promos["t1"].Code != "SAVE" {
		t.Fatalf("скидка группы: %+v %v", state, err)
	}
	if _, ok := state.Promos["t2"]; ok {
		t.Fatal("код попал в другой магазин")
	}
	message := "минимальная сумма не достигнута"
	e.platform.SetPromoPricing("t1", ports.GroupPricing{PromoCode: &cart.PromoCode{Code: "SAVE", Message: &message}})
	state, err = e.service.SetQuantity(ctx, buyerID, p1, 1)
	if err != nil || state.Promos["t1"].Valid || len(state.Lines[0].Discounts) != 0 {
		t.Fatalf("перепроверка: %+v %v", state, err)
	}
	state, err = e.service.SetPromo(ctx, buyerID, "t1", "")
	if err != nil || len(state.Promos) != 0 {
		t.Fatalf("удаление: %+v %v", state, err)
	}
	if _, err = e.service.SetPromo(ctx, buyerID, "absent", "SAVE"); !errors.Is(err, cart.ErrLineNotFound) {
		t.Fatal(err)
	}
}

func TestPromoLastLineAndExpiry(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.service.Add(ctx, buyerID, p1, 1)
	_, _ = e.service.SetPromo(ctx, buyerID, "t1", "BAD")
	state, err := e.service.Remove(ctx, buyerID, p1)
	if err != nil || len(state.Promos) != 0 {
		t.Fatalf("оставшийся код: %+v %v", state, err)
	}
}

func TestPromoVerificationFailureKeepsSnapshot(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, _ = e.service.Add(ctx, buyerID, p1, 1)
	original, err := e.service.SetPromo(ctx, buyerID, "t1", "BAD")
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("нет ответа")
	e.platform.Fail("t1", failure)
	if _, err := e.service.SetPromo(ctx, buyerID, "t1", "NEW"); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID)
	if err != nil || state.Promos["t1"].Code != "BAD" || state.Version != original.Version {
		t.Fatal(state, err)
	}
	e.clock.Advance(31 * 24 * time.Hour)
	state, err = e.service.Get(ctx, buyerID)
	if err != nil || len(state.Promos) != 0 || len(state.Lines) != 0 {
		t.Fatal(state, err)
	}
}

func TestPricingOnlyAffectedTenantAndVerifyOnce(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for _, offer := range []ids.OfferID{p1, p2} {
		if _, err := e.service.Add(ctx, buyerID, offer, 1); err != nil {
			t.Fatal(err)
		}
	}
	calls := len(e.platform.ItemCheckCalls())
	if calls != 2 {
		t.Fatal("ожидался один расчёт на изменение", calls)
	}
	if _, err := e.service.Verify(ctx, buyerID); err != nil {
		t.Fatal(err)
	}
	if len(e.platform.ItemCheckCalls()) != calls+2 {
		t.Fatal("Verify должен вызвать каждый магазин один раз")
	}
	e.platform.Fail("t1", errors.New("магазин недоступен"))
	if _, err := e.service.SetQuantity(ctx, buyerID, p2, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.SetPromo(ctx, buyerID, "t2", "BAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Add(ctx, buyerID, p2, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Remove(ctx, buyerID, p2); err != nil {
		t.Fatal(err)
	}
}

func TestMutationAcceptsServerPriceAndStock(t *testing.T) {
	for _, set := range []bool{false, true} {
		for _, scenario := range []string{"price", "missing", "stock"} {
			t.Run(scenario+map[bool]string{false: "Add", true: "SetQuantity"}[set], func(t *testing.T) {
				e := setup(t)
				ctx := context.Background()
				if set {
					if _, err := e.service.Add(ctx, buyerID, p1, 1); err != nil {
						t.Fatal(err)
					}
				}
				e.platform.SetPromoPricing("t1", ports.GroupPricing{Items: []ports.ItemCheck{{
					ProductID: p1.Product,
					Exists:    scenario != "missing",
					Available: map[bool]int{false: 10, true: 0}[scenario == "stock"],
					Price:     money.FromKopecks(123),
				}}})
				var state cart.State
				var err error
				if set {
					state, err = e.service.SetQuantity(ctx, buyerID, p1, 2)
				} else {
					state, err = e.service.Add(ctx, buyerID, p1, 2)
				}
				switch scenario {
				case "missing":
					if !errors.Is(err, cart.ErrOfferUnavailable) {
						t.Fatal(err)
					}
				case "stock":
					if !errors.Is(err, cart.ErrNotEnoughStock) {
						t.Fatal(err)
					}
				default:
					if err != nil || state.Lines[0].Price.Kopecks() != 123 || state.Lines[0].Issue != cart.IssueNone {
						t.Fatal(state, err)
					}
				}
			})
		}
	}
}

func TestUnavailableSellerAllowsRemoval(t *testing.T) {
	for _, zero := range []bool{false, true} {
		t.Run(map[bool]string{false: "Remove", true: "SetQuantity"}[zero], func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			second := ids.OfferID{Tenant: "t1", Product: "p2"}
			err := e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				basket := cart.New("cart", buyerID, e.clock.Now(), time.Hour)
				for _, offer := range []ids.OfferID{p1, second} {
					if err := basket.Add(
						cart.Offer{
							ID:        offer,
							Name:      "Товар",
							Price:     money.FromKopecks(100),
							Available: 10,
							Visible:   true,
						},
						1,
						e.clock.Now(),
						time.Hour,
					); err != nil {
						return err
					}
				}
				basket.ApplyPromo(
					"t1",
					cart.PromoCode{Code: "SAVE", Valid: true},
					map[ids.ProductID][]cart.Discount{
						"p2": {{PromotionID: "promo", Amount: money.FromKopecks(10)}},
					},
					e.clock.Now(),
					time.Hour,
				)
				return tx.Carts().Save(ctx, basket)
			})
			if err != nil {
				t.Fatal(err)
			}
			e.platform.Fail("t1", ports.ErrMarketplaceDisabled)
			remove := func(id ids.OfferID) (cart.State, error) {
				if zero {
					return e.service.SetQuantity(ctx, buyerID, id, 0)
				}
				return e.service.Remove(ctx, buyerID, id)
			}
			state, err := remove(p1)
			if err != nil || len(state.Lines) != 1 || state.Promos["t1"].Valid ||
				state.Promos["t1"].Message != nil || len(state.Lines[0].Discounts) != 0 {
				t.Fatal(state, err)
			}
			saved, err := e.service.Get(ctx, buyerID)
			if err != nil || len(saved.Lines) != 1 {
				t.Fatal(saved, err)
			}
			state, err = remove(second)
			if err != nil || len(state.Lines) != 0 || len(state.Promos) != 0 {
				t.Fatal(state, err)
			}
		})
	}
}

func TestUnavailableSellerAllowsPromoRemoval(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.service.Add(ctx, buyerID, p1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.SetPromo(ctx, buyerID, "t1", "SAVE"); err != nil {
		t.Fatal(err)
	}
	e.platform.Fail("t1", ports.ErrMarketplaceDisabled)
	state, err := e.service.SetPromo(ctx, buyerID, "t1", "")
	if err != nil || len(state.Promos) != 0 {
		t.Fatal(state, err)
	}
	e.platform.Fail("t1", nil)
	calls := len(e.platform.ItemCheckCalls())
	if _, err := e.service.SetPromo(ctx, buyerID, "t1", ""); err != nil {
		t.Fatal(err)
	}
	if len(e.platform.ItemCheckCalls()) != calls {
		t.Fatal("пустой код вызвал сервер")
	}
}
