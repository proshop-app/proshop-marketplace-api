package participant_test

import (
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func TestCardProductsValidationAndIsolation(t *testing.T) {
	p := newParticipant(t)
	if products := p.State().StoreCard.ProductSettings(); !products.Enabled || products.ProductIDs == nil {
		t.Fatal(products)
	}
	products := participant.CardProducts{Enabled: true, ProductIDs: []string{"p2", "p1"}}
	if changed, err := p.ApplyCardProducts(products, t0); !changed || err != nil {
		t.Fatal(changed, err)
	}
	products.ProductIDs[0] = "mutated"
	state := p.State().StoreCard.Clone()
	state.Products.ProductIDs[0] = "also-mutated"
	if actual := p.State().StoreCard.ProductSettings(); actual.ProductIDs[0] != "p2" {
		t.Fatal(actual)
	}
	products = participant.CardProducts{Enabled: false, ProductIDs: []string{"p2", "p1"}}
	if changed, err := p.ApplyCardProducts(products, t1); !changed || err != nil {
		t.Fatal(changed, err)
	}
	if changed, err := p.ApplyCardProducts(products, t1); changed || err != nil {
		t.Fatal(changed, err)
	}
	products.Enabled = true
	for _, ids := range [][]string{{"p1", "p1"}, {""}, {"invalid id"}, {"p1", "p2", "p3", "p4"}} {
		products.ProductIDs = ids
		if _, err := p.ApplyCardProducts(products, t1); !errors.Is(err, participant.ErrInvalidCardProducts) {
			t.Fatal(ids, err)
		}
	}
}
