package cart_test

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestPromoIdempotenceAndAmounts(t *testing.T) {
	c := newCart()
	if err := c.Add(offer("t1", "p1", 100, 10), 2, t0, ttl); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		changed, err := c.SetPromo("t1", " SAVE ", t0, ttl)
		if err != nil || changed != (i == 0) {
			t.Fatal(changed, err)
		}
	}
	promo := cart.PromoCode{Code: "SAVE", Valid: true, Discount: money.FromKopecks(10)}
	discounts := map[ids.ProductID][]cart.Discount{"p1": {{PromotionID: "promo", Amount: money.FromKopecks(10)}}}
	for i := 0; i < 2; i++ {
		if changed := c.ApplyPromo("t1", promo, discounts, t0, ttl); changed != (i == 0) {
			t.Fatal(changed)
		}
	}
	total, err := c.Total()
	if err != nil || total.Kopecks() != 190 {
		t.Fatal(total, err)
	}
	for _, amount := range []int64{-1, 201} {
		_, err := cart.DiscountedAmount(money.FromKopecks(100), 2, []cart.Discount{{Amount: money.FromKopecks(amount)}})
		if err == nil {
			t.Fatal("недопустимая скидка принята", amount)
		}
	}
	c.Consume(ids.OfferID{Tenant: "t1", Product: "p1"}, 1, t0, ttl)
	if c.State().Promos["t1"].Valid || len(c.Lines()[0].Discounts) != 0 {
		t.Fatal(c.State())
	}
}
