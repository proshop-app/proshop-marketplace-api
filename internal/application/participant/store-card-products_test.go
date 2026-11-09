package participant_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestStoreCardProductsHybridSelection(t *testing.T) {
	store, service := seedStoreCard(t)
	ctx := context.Background()
	for i, id := range []ids.ProductID{"p1", "p2", "p3", "p4"} {
		state, _ := store.Product("t1", id)
		state.ProjectedAt = t0.Add(time.Duration(i) * time.Hour)
		if id == "p2" {
			state.SalesCount = 20
		}
		store.SeedProduct(state)
	}
	state, _ := store.Participant("t1")
	pins := participant.CardProducts{Enabled: true, ProductIDs: []string{"p4", "p2", "other-shop"}}
	state.StoreCard.Products = &pins
	store.SeedParticipant(state)
	refreshStore(t, service)
	summary, err := service.StoreSummary(ctx, "t1")
	if err != nil || !reflect.DeepEqual(cardProductIDs(summary.Products), []string{"p2", "p1", "p4"}) ||
		!reflect.DeepEqual(cardProductIDs(summary.SelectedProducts), []string{"p4", "p2"}) {
		t.Fatal(summary, err)
	}
	page, err := service.Shops(ctx, "", 20, 0)
	if err != nil || len(page.Items) != 1 ||
		!reflect.DeepEqual(cardProductIDs(page.Items[0].Products), []string{"p4", "p2", "p1"}) {
		t.Fatal(page, err)
	}
	products := page.Items[0].Products
	if products[0].OfferID != "t1~p4" || products[0].Name != "Товар" || products[0].Image != nil ||
		products[2].Image == nil || *products[2].Image != "image" {
		t.Fatal(products)
	}
	pins.Enabled = false
	state.StoreCard.Products = &pins
	store.SeedParticipant(state)
	refreshStore(t, service)
	page, err = service.Shops(ctx, "", 20, 0)
	if err != nil || len(page.Items[0].Products) != 0 || page.Items[0].Products == nil {
		t.Fatal(page, err)
	}
	summary, err = service.StoreSummary(ctx, "t1")
	if err != nil || len(summary.Products) != 3 || len(summary.SelectedProducts) != 0 {
		t.Fatal(summary, err)
	}
}

func TestStoreCardProductsNeverUsePrivateCandidates(t *testing.T) {
	store, service := seedStoreCard(t)
	state, _ := store.Participant("t1")
	pins := participant.CardProducts{Enabled: true, ProductIDs: []string{"p1", "p2", "p3"}}
	state.StoreCard.Products = &pins
	store.SeedParticipant(state)
	store.SeedVerdict(moderation.VerdictState{TenantID: "t1", ProductID: "p1", Status: moderation.Rejected})
	product, _ := store.Product("t1", "p2")
	product.Deleted = true
	store.SeedProduct(product)
	product, _ = store.Product("t1", "p3")
	product.Card.Visible = false
	store.SeedProduct(product)
	refreshStore(t, service)
	summary, err := service.StoreSummary(context.Background(), "t1")
	if err != nil || !reflect.DeepEqual(cardProductIDs(summary.Products), []string{"p4"}) ||
		len(summary.SelectedProducts) != 0 {
		t.Fatal(summary, err)
	}
	state.Stage = participant.StageCandidate
	store.SeedParticipant(state)
	refreshStore(t, service)
	summary, err = service.StoreSummary(context.Background(), "t1")
	if err != nil || summary.Offers == 0 || len(summary.Products) != 0 || len(summary.SelectedProducts) != 0 {
		t.Fatal("статистика кандидата не должна открывать товары в карточке", summary, err)
	}
}

func TestStoreCardProductsDefaultAndStableFallback(t *testing.T) {
	store, service := seedStoreCard(t)
	for _, id := range []ids.ProductID{"p1", "p2", "p3", "p4"} {
		state, _ := store.Product("t1", id)
		state.Card.Image = ""
		state.Revision = 1
		state.ProjectedAt = t0
		store.SeedProduct(state)
	}
	refreshStore(t, service)
	page, err := service.Shops(context.Background(), "", 20, 0)
	if err != nil || len(page.Items) != 1 ||
		!reflect.DeepEqual(cardProductIDs(page.Items[0].Products), []string{"p1", "p2", "p3"}) {
		t.Fatal(page, err)
	}
	product, _ := store.Product("t1", "p4")
	product.ProjectedAt = t0.Add(time.Minute)
	store.SeedProduct(product)
	refreshStore(t, service)
	page, err = service.Shops(context.Background(), "", 20, 0)
	if err != nil || !reflect.DeepEqual(cardProductIDs(page.Items[0].Products), []string{"p4", "p1", "p2"}) {
		t.Fatal(page, err)
	}
}

func cardProductIDs(products []ports.StoreCardProduct) []string {
	ids := []string{}
	for _, product := range products {
		ids = append(ids, product.ProductID)
	}
	return ids
}
