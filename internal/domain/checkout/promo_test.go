package checkout_test

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestRepriceAppliesDiscountsAndRecounts(t *testing.T) {
	c := draft(t)
	checks := make(map[ids.OfferID]checkout.PriceCheck)
	for _, group := range c.Groups() {
		for _, line := range group.Lines {
			checks[line.Offer] = checkout.PriceCheck{
				Exists:    true,
				Available: line.Quantity,
				Price:     money.FromKopecks(100),
				Discounts: []cart.Discount{{PromotionID: "auto", Amount: money.FromKopecks(10)}},
			}
		}
	}
	for i := 0; i < 2; i++ {
		changed, err := c.Reprice(checks, t0)
		if err != nil || changed != (i == 0) {
			t.Fatal(changed, err)
		}
	}
	state := c.State()
	if state.Groups[0].Goods.Kopecks() != 280 || state.Groups[1].Goods.Kopecks() != 90 ||
		state.GoodsTotal.Kopecks() != 370 || state.Total.Kopecks() != 370 {
		t.Fatal(state)
	}
	checks[state.Groups[0].Lines[0].Offer] = checkout.PriceCheck{
		Exists:    true,
		Available: 10,
		Price:     money.FromKopecks(1),
		Discounts: []cart.Discount{{Amount: money.FromKopecks(100)}},
	}
	if _, err := c.Reprice(checks, t0); err == nil {
		t.Fatal("скидка больше суммы принята")
	}
	if c.State().Total != state.Total {
		t.Fatal("некорректный ответ изменил снимок")
	}
}

func TestSetPromoCodesIdempotent(t *testing.T) {
	c := draft(t)
	codes := map[ids.TenantID]string{c.Groups()[0].TenantID: "SAVE"}
	for i := 0; i < 2; i++ {
		version := c.State().Version
		changed, err := c.SetPromoCodes(codes, t0)
		if err != nil || changed != (i == 0) {
			t.Fatal(changed, err)
		}
		if i == 1 && c.State().Version != version {
			t.Fatal("повтор изменил версию")
		}
	}
}
