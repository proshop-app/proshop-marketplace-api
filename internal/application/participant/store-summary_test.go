package participant_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestAutomaticShopProductsExcludePrivateOffers(t *testing.T) {
	store, service := seedStoreCard(t)
	state, _ := store.Participant("t1")
	store.SeedVerdict(moderation.VerdictState{TenantID: "t1", ProductID: "p1", Status: moderation.Rejected})
	product, _ := store.Product("t1", "p2")
	product.Deleted = true
	store.SeedProduct(product)
	product, _ = store.Product("t1", "p3")
	product.Card.Visible = false
	store.SeedProduct(product)
	refreshStore(t, service)
	summary, err := service.StoreSummary(context.Background(), "t1")
	if err != nil || !reflect.DeepEqual(cardProductIDs(summary.Products), []string{"p4"}) {
		t.Fatal(summary, err)
	}
	state.Stage = participant.StageCandidate
	store.SeedParticipant(state)
	refreshStore(t, service)
	summary, err = service.StoreSummary(context.Background(), "t1")
	if err != nil || summary.Offers == 0 || len(summary.Products) != 0 {
		t.Fatal("статистика кандидата не должна открывать товары в карточке", summary, err)
	}
}

func TestAutomaticShopProductsStableRanking(t *testing.T) {
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

func cardProductIDs(products []ports.ShopCardProduct) []string {
	ids := []string{}
	for _, product := range products {
		ids = append(ids, product.ProductID)
	}
	return ids
}

func TestStoreSummaryAcrossPagesKeepsOneRatingPerVariantGroup(t *testing.T) {
	store, service := seedStoreCard(t)
	template, _ := store.Product("t1", "p1")
	for i := 0; i < 450; i++ {
		product := template
		product.ProductID = ids.ProductID(fmt.Sprintf("bulk-%03d", i))
		product.Card.VariantsID = "shared"
		product.AverageRating = 4
		product.RatingsCount = 10
		product.SalesCount = int64(i)
		store.SeedProduct(product)
		store.SeedVerdict(moderation.VerdictState{
			TenantID:  "t1",
			ProductID: product.ProductID,
			Status:    moderation.Allowed,
		})
	}
	refreshStore(t, service)
	summary, err := service.StoreSummary(context.Background(), "t1")
	if err != nil || summary.Offers != 454 || summary.Rating == nil || summary.Rating.Count != 10 ||
		summary.Rating.Value != 4 || len(summary.Products) != 3 || summary.Products[0].ProductID != "bulk-449" {
		t.Fatal(summary, err)
	}
}
