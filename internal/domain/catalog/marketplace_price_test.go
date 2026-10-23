package catalog_test

import (
	"encoding/json"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
)

func TestMarketplacePriceSurvivesShopPriceSignals(t *testing.T) {
	s := snapshot(1, t0)
	price := json.Number("249.995")
	s.Card.Marketplace.Price = &price
	product, err := catalog.NewProduct(s, t0)
	if err != nil {
		t.Fatal(err)
	}
	signals := catalog.NewSignals("t1", "p1")
	if _, err := signals.ApplyPrice(catalog.PriceSignal{Price: rub(5000), At: t1}, t1); err != nil {
		t.Fatal(err)
	}
	offer := catalog.ComposeOffer(product, signals, visibleSeller)
	if offer.Price.Kopecks() != 25000 {
		t.Fatalf("цена площадки: %v", offer.Price)
	}
	next := snapshot(2, t2)
	if _, err := product.Apply(next, t2); err != nil {
		t.Fatal(err)
	}
	offer = catalog.ComposeOffer(product, signals, visibleSeller)
	if offer.Price.Kopecks() != 10000 {
		t.Fatalf("отсутствие цены площадки — цена магазина: %v", offer.Price)
	}
}
