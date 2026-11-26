package checkout_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func TestSellersForResponse(t *testing.T) {
	e := setup(t)
	e.store.SeedParticipant(participant.State{TenantID: "t1", Slug: "shop-one", Name: "Магазин"})
	e.store.SeedParticipant(participant.State{TenantID: "t2", Slug: "shop-two", Name: "Другой магазин"})
	state := []checkout.State{
		{Groups: []checkout.Group{{TenantID: "t1"}, {TenantID: "missing"}}},
		{Groups: []checkout.Group{{TenantID: "t1"}, {TenantID: "t2"}}},
	}
	sellers, err := e.service.Sellers(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	want := participant.Seller{Slug: "shop-one", Name: "Магазин", UnavailableReason: "delivery_unavailable"}
	if sellers.Items["t1"] == nil || *sellers.Items["t1"] != want {
		t.Fatal(sellers)
	}
	if sellers.Items["missing"] != nil {
		t.Fatal(sellers)
	}
	if sellers.Items["t2"] == nil || sellers.Items["t2"].Slug != "shop-two" {
		t.Fatal(sellers)
	}
}
