package taxonomydata

import (
	"context"
	"slices"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestBundledCatalogFacetLayout(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	d := release.Definition()
	store := fake.NewStore()
	store.SeedTaxonomy(d)
	queries := appcatalog.NewQueries(store, fake.NewIndex(), nil, nil, nil, fake.NewClock(time.Time{}))
	for id, want := range map[string][]string{
		"":            {"brand"},
		"electronics": {"brand"},
		"clothing":    {"brand", "color"},
		"electronics.smartphones": {
			"storage_capacity",
			"ram",
			"brand",
			"screen_diagonal",
			"processor",
			"battery_capacity",
			"esim_support",
			"wireless_charge_support",
			"color",
			"nfc",
			"refresh_rate",
			"screen_matrix",
			"camera_megapixels",
			"os",
			"external_memory_type",
			"network_standard",
			"operating_time",
			"ip_rating",
		},
	} {
		t.Run(id, func(t *testing.T) {
			prepared, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{CategoryID: id})
			if err != nil {
				t.Fatal(err)
			}
			codes := make([]string, 0, len(prepared.FacetFields))
			for _, field := range prepared.FacetFields {
				codes = append(codes, field.Code)
			}
			if !slices.Equal(codes, want) {
				t.Fatalf("фильтры %v, ожидаются %v", codes, want)
			}
		})
	}
	field := d.Fields()["esim_support"]
	if field.Facet || !slices.Contains(release.FacetKeys, field.Code) {
		t.Fatal("локальное включение eSIM не дошло до набора ключей индекса")
	}
	node, _, found := d.Category("electronics")
	priorities := []taxonomy.Filter{
		{Code: taxonomy.FilterPrice, Primary: true},
		{Code: "brand", Primary: true},
		{Code: taxonomy.FilterInStock, Primary: true},
		{Code: taxonomy.FilterSeller, Primary: true},
		{Code: taxonomy.FilterCategory, Primary: true},
	}
	if !found || !slices.Equal(node.PriorityFilters, priorities) {
		t.Fatalf("приоритеты электроники: %v", node.PriorityFilters)
	}
}
