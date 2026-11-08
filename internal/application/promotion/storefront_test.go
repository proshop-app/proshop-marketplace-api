package promotion_test

import (
	"context"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	app "github.com/proshop-app/proshop-marketplace/internal/application/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestSponsoredPrepareQueryAndPrices(t *testing.T) {
	e := setup()
	ctx := context.Background()
	c := e.create(t, "category_slot")
	c.Status = promotion.Active
	e.store.SeedPromotion(c)
	e.clock.Advance(72 * time.Hour)
	query := ports.OfferQuery{
		CategoryID:    "c1",
		PriceContexts: map[ids.TenantID]string{"t1": catalog.DefaultPriceProfile},
	}
	rows, err := e.service.Sponsored(ctx, query)
	if err != nil || len(rows) != 1 || rows[0].Offer.Price != 100 {
		t.Fatal(rows, err)
	}
	e.store.SeedTaxonomy(taxonomy.Definition{
		Attributes: []taxonomy.Attribute{{Code: "a1", Type: "enum", Facet: true}},
		Version:    1,
		Categories: []taxonomy.Category{{ID: "c1", Schema: "s1"}},
		Schemas:    map[string]taxonomy.Schema{"s1": {}},
	})
	service := app.NewService(e.store, e.clock, &fake.IDs{}, e.index, app.Config{
		Location:    time.UTC,
		SearchDepth: 100,
		Random:      fake.Random{},
		Catalog:     appcatalog.NewQueries(e.store, e.index, nil, nil, nil, fake.NewClock(time.Time{})),
	})
	if _, err := service.Sponsored(ctx, ports.OfferQuery{Text: "Товар", TrackQuery: true}); err != nil {
		t.Fatal(err)
	}
	if e.index.Query.TrackQuery || e.index.Query.Text != "Товар" {
		t.Fatalf("рекламный поиск после PrepareQuery: %+v", e.index.Query)
	}
	if e.index.Query.Facets == nil || len(*e.index.Query.Facets) != 0 || len(e.index.Query.FacetFields) != 0 {
		t.Fatalf("рекламный подбор запросил ненужные фасеты: %+v", e.index.Query)
	}

	if _, err := service.Sponsored(ctx, ports.OfferQuery{CategoryID: "unknown"}); err == nil {
		t.Fatal("PrepareQuery должен отклонить неизвестную категорию")
	}
	rows, err = service.Sponsored(ctx, ports.OfferQuery{CategoryID: "c1"})
	if err != nil || len(rows) != 1 || rows[0].Offer.Price != 100 {
		t.Fatal(rows, err)
	}
}
