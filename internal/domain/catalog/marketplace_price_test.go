package catalog_test

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
)

func TestMarketplacePriceSurvivesShopPriceSignals(t *testing.T) {
	s := snapshot(1, t0)
	s.PriceProfiles = []catalog.PriceProfile{
		{
			Key:            catalog.DefaultPriceProfile,
			Price:          rub(25000),
			EffectivePrice: rub(25000),
		},
	}
	product, err := catalog.NewProduct(s, t0)
	if err != nil {
		t.Fatal(err)
	}
	signals := catalog.NewSignals("t1", "p1")
	_, _ = signals.ApplyPrice(catalog.PriceSignal{Price: rub(5000), At: t1}, t1)
	offer := catalog.ComposeOffer(product, signals, visibleSeller)
	if offer.Price.Kopecks() != 25000 {
		t.Fatalf("цена канала Pricing: %v", offer.Price)
	}
	next := snapshot(2, t2)
	if _, err := product.Apply(next, t2); err != nil {
		t.Fatal(err)
	}
	offer = catalog.ComposeOffer(product, signals, visibleSeller)
	if offer.Price.Kopecks() != 10000 {
		t.Fatalf("новая проекция Pricing: %v", offer.Price)
	}
}
