//go:build integration

package searchapi_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/searchapi"
)

func TestListingWindowThroughSearchClient(t *testing.T) {
	index := testSearchIndex(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	generation, err := index.NewGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for start := 0; start < 2401; start += 200 {
		batch := make([]ports.OfferDocument, 0, 200)
		for i := start; i < min(start+200, 2401); i++ {
			product := fmt.Sprintf("listing-%04d", i)
			price := int64(100 + i)
			batch = append(batch, ports.OfferDocument{
				ID:           "listing-shop~" + product,
				TenantID:     "listing-shop",
				ProductID:    product,
				Name:         "Куртка",
				CategoryID:   "jackets",
				CategoryPath: []string{"jackets"},
				UpdatedAt:    time.Unix(100, 0).UTC(),
				Version:      1,
				Visible:      true,
				Listed:       true,
				Price:        price,
				BasePrice:    price,
			})
		}
		if err := index.PutGeneration(ctx, generation, batch); err != nil {
			t.Fatal(err)
		}
	}
	if err := index.Activate(ctx, generation); err != nil {
		t.Fatal(err)
	}

	last, err := index.Search(ctx, ports.OfferQuery{
		TenantIDs: []string{"listing-shop"},
		Offset:    2399,
		Limit:     20,
		Sort:      ports.SortPriceAsc,
	})
	if err != nil || last.Total != 2401 || last.TotalRelation != "eq" ||
		!last.Limited || len(last.Items) != 1 || last.Items[0].ProductID != "listing-2399" {
		t.Fatalf("последняя порция: %+v, ошибка %v", last, err)
	}
	if _, err := index.Search(ctx, ports.OfferQuery{Offset: 2400, Limit: 20}); !errors.Is(err, searchapi.ErrInvalid) {
		t.Fatalf("смещение 2400: ожидалась ошибка запроса, получено %v", err)
	}

	facets, err := index.Search(ctx, ports.OfferQuery{
		FacetKeys:      []string{"price"},
		PriceHistogram: true,
		Limit:          20,
	})
	if err != nil || len(facets.Items) != 0 || len(facets.Facets) != 1 ||
		facets.Facets[0].Code != "price" || len(facets.Facets[0].Buckets) == 0 {
		t.Fatalf("гистограмма цены: %+v, ошибка %v", facets, err)
	}
}
