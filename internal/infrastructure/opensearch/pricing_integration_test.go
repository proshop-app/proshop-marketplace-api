//go:build integration

package opensearch_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestPricingBeforePaginationAndFacets(t *testing.T) {
	ctx := context.Background()
	index := testIndex(t)
	generation, err := index.NewGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	group := "marketplace:111111111111111111111111"
	docs := []ports.OfferDocument{
		offer("t1~p1", "Товар 1", 10000, 1),
		offer("t1~p2", "Товар 2", 5000, 1),
		offer("t2~p1", "Товар 3", 2000, 1),
	}
	for i := range docs {
		value := []int64{0, 8000, 9000}[i]
		docs[i].PriceProfiles = append(docs[i].PriceProfiles, catalog.PriceProfile{
			Key:            group,
			Price:          money.FromKopecks(value),
			EffectivePrice: money.FromKopecks(value),
		})
	}
	if err := index.PutGeneration(ctx, generation, docs); err != nil {
		t.Fatal(err)
	}
	if err := index.Activate(ctx, generation); err != nil {
		t.Fatal(err)
	}
	if err := index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	contexts := map[ids.TenantID]string{
		"t1": group,
		"t2": catalog.DefaultPriceProfile,
	}
	page, err := index.Search(ctx, ports.OfferQuery{
		PriceContexts: contexts,
		Sort:          ports.SortPriceAsc,
		Limit:         1,
	})
	if err != nil || page.Total != 3 || len(page.Items) != 1 ||
		page.Items[0].ID != "t1~p1" || page.Items[0].Price != 0 {
		t.Fatalf("сортировка и пагинация по применимой цене: %+v %v", page, err)
	}
	var price ports.Facet
	for _, facet := range page.Facets {
		if facet.Code == "price" {
			price = facet
		}
	}
	if price.Min == nil || price.Max == nil || price.Min.String() != "0" ||
		price.Max.String() != "8000" || price.Count != 3 {
		t.Fatalf("диапазон считается до пагинации по одному профилю каждого магазина: %+v", price)
	}
	lower := int64(6000)
	page, err = index.Search(ctx, ports.OfferQuery{
		PriceContexts: contexts,
		PriceMin:      &lower,
		Sort:          ports.SortPriceAsc,
		Limit:         10,
	})
	if err != nil || page.Total != 1 || page.Items[0].ID != "t1~p2" || page.Items[0].Price != 8000 {
		t.Fatalf("нижняя граница не использует чужой прайс: %+v %v", page, err)
	}
}
