package catalog_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestDisjunctiveFacets(t *testing.T) {
	ctx := context.Background()
	index := fake.NewIndex()
	for i, color := range []string{"black", "red", "white", "black"} {
		tenant, size := "t1", "s"
		if i > 1 {
			tenant, size = "t2", "m"
		}
		price := money.FromKopecks(int64(i+1) * 100)
		doc := ports.OfferDocument{
			ID:           color + size,
			TenantID:     tenant,
			Name:         "Товар",
			CategoryID:   "c1",
			CategoryPath: []string{"c1"},
			VariantsID:   "g1",
			Listed:       true,
			InStock:      i != 2,
			Attributes: map[string]json.RawMessage{
				"color": json.RawMessage(`"` + color + `"`),
				"size":  json.RawMessage(`"` + size + `"`),
			},
			PriceProfiles: []catalog.PriceProfile{
				{Key: catalog.DefaultPriceProfile, Price: price, EffectivePrice: price},
			},
		}
		// Числовой атрибут независим от денежного представления.
		doc.Attributes["n"] = json.RawMessage([]string{"1", "2", "3", "4"}[i])
		if err := index.Put(ctx, []ports.OfferDocument{doc}); err != nil {
			t.Fatal(err)
		}
	}
	query := ports.OfferQuery{
		Limit: 10,
		FacetFields: []taxonomy.Attribute{
			{Code: "color", Type: "enum"},
			{Code: "size", Type: "enum"},
			{Code: "n", Type: "number"},
		},
		Attributes: map[string]ports.AttributeFilter{
			"color": {Values: []string{"black"}},
		},
	}
	check := func(total int, code string, values map[string]int) ports.Facet {
		t.Helper()
		page, err := index.Search(ctx, query)
		if err != nil || page.Total != total || len(page.Items) != total {
			t.Fatalf("выдача: %+v, %v", page, err)
		}
		facet := findFacet(t, page.Facets, code)
		if values != nil {
			got := map[string]int{}
			for _, bucket := range facet.Values {
				got[bucket.Value] = bucket.Count
			}
			if !reflect.DeepEqual(got, values) {
				t.Fatalf("%s: %v, ожидалось %v", code, got, values)
			}
		}
		return facet
	}
	check(2, "attr.color", map[string]int{"black": 2, "red": 1, "white": 1})
	query.Attributes["color"] = ports.AttributeFilter{Values: []string{"black", "red"}}
	check(3, "attr.color", map[string]int{"black": 2, "red": 1, "white": 1})
	query.Attributes["size"] = ports.AttributeFilter{Values: []string{"s"}}
	check(2, "attr.color", map[string]int{"black": 1, "red": 1})
	check(2, "attr.size", map[string]int{"s": 2, "m": 1})
	query.Attributes = nil
	query.TenantIDs = []string{"t1"}
	check(2, "tenant", map[string]int{"t1": 2, "t2": 2})
	query.TenantIDs = []string{"t1", "t2"}
	check(4, "tenant", map[string]int{"t1": 2, "t2": 2})
	low, high := int64(200), int64(300)
	query.PriceMin, query.PriceMax = &low, &high
	facet := check(2, "price", nil)
	if facet.Min.String() != "100" || facet.Max.String() != "400" {
		t.Fatal(facet)
	}
	query.PriceMin, query.PriceMax = nil, nil
	query.Attributes = map[string]ports.AttributeFilter{"n": {Min: "2", Max: "3"}}
	facet = check(2, "attr.n", nil)
	if facet.Min.String() != "1" || facet.Max.String() != "4" {
		t.Fatal(facet)
	}
	query.Attributes = nil
	query.InStockSet = true
	check(1, "inStock", map[string]int{"true": 3, "false": 1})
	check(1, "category", map[string]int{"c1": 1})
	query.GroupID = "g1"
	query.TenantIDs = []string{"t1"}
	query.InStockSet = false
	check(2, "tenant", map[string]int{"t1": 2})
	query.GroupID = "other"
	check(0, "tenant", map[string]int{})
	query.GroupID = ""
	query.CategoryID = "other"
	check(0, "tenant", map[string]int{})
	query.CategoryID = ""
	query.Text = "нет"
	check(0, "inStock", map[string]int{})
	query.Text = ""
	query.PriceContexts = map[ids.TenantID]string{}
	check(0, "tenant", map[string]int{})
}

func findFacet(t *testing.T, facets []ports.Facet, code string) ports.Facet {
	t.Helper()
	for _, facet := range facets {
		if facet.Code == code {
			return facet
		}
	}
	t.Fatalf("нет фасета %s: %+v", code, facets)
	return ports.Facet{}
}

func TestTenantFacetPriceContexts(t *testing.T) {
	ctx := context.Background()
	store, index, platform := fake.NewStore(), fake.NewIndex(), fake.NewPlatform()
	for _, tenant := range []ids.TenantID{"t1", "t2"} {
		seedSeller(store, tenant, participant.TenantActive, true)
		price := money.FromKopecks(100)
		key := catalog.DefaultPriceProfile
		if tenant == "t2" {
			key = "marketplace:buyer"
		}
		err := index.Put(ctx, []ports.OfferDocument{
			{
				ID:       string(tenant),
				TenantID: string(tenant),
				Listed:   true,
				PriceProfiles: []catalog.PriceProfile{
					{Key: key, Price: price, EffectivePrice: price},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	platform.SetBuyerProfile("buyer", "t2", "marketplace:buyer")
	queries := appcatalog.NewQueries(store, index, platform, nil, nil, fake.NewClock(time.Time{}))
	query := ports.OfferQuery{TenantIDs: []string{"t1"}}
	for _, buyer := range []ids.UUID{"", "buyer"} {
		query.BuyerID = buyer
		page, err := queries.Search(ctx, query)
		if err != nil || page.Total != 1 || page.Items[0].Offer.TenantID != "t1" {
			t.Fatalf("%+v %v", page, err)
		}
		want := 1
		if buyer != "" {
			want = 2
		}
		facet := findFacet(t, page.Facets, "tenant")
		if facet.Count != want || len(facet.Values) != want {
			t.Fatalf("покупатель %s: %+v", buyer, facet)
		}
	}
	query.GroupID = "g1"
	prepared, err := queries.PrepareQuery(ctx, query)
	if err != nil || len(prepared.PriceContexts) != 1 || prepared.PriceContexts["t1"] == "" {
		t.Fatalf("контекст группы: %+v %v", prepared, err)
	}
}
