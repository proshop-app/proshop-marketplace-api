package cart_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestSellersForResponse(t *testing.T) {
	e := setup(t)
	e.store.SeedParticipant(participant.State{TenantID: "t1", Slug: "shop-one", Name: "Магазин"})
	e.store.SeedParticipant(participant.State{TenantID: "t2", Slug: "shop-two", Name: "Другой магазин"})
	state := cart.State{Lines: []cart.Line{
		{Offer: ids.OfferID{Tenant: "t1", Product: "p1"}},
		{Offer: ids.OfferID{Tenant: "t1", Product: "p2"}},
		{Offer: ids.OfferID{Tenant: "missing", Product: "p1"}},
	}}
	sellers, err := e.service.Sellers(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if sellers["t1"] == nil || *sellers["t1"] != (participant.Seller{Slug: "shop-one", Name: "Магазин"}) {
		t.Fatal(sellers)
	}
	if sellers["missing"] != nil {
		t.Fatal(sellers)
	}

}
