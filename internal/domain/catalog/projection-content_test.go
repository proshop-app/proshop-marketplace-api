package catalog_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestIdenticalProjectionContentIgnoresDeliveryAndDerivedSearchFields(t *testing.T) {
	previous := reviewedProjectionContent(t)
	current := reviewedProjectionContent(t)
	current.Revision++
	current.ProjectedAt = current.ProjectedAt.Add(time.Hour)
	current.Version++
	current.UpdatedAt = current.UpdatedAt.Add(time.Hour)
	current.Card.ModelKey = ""
	current.Card.ModelKeySource = ""
	current.Card.Publication.Version++
	current.Card.Publication.AttributeSearchTerms = map[string][]string{"a1": {"новое выражение"}}
	if !catalog.IdenticalProjectionContent(previous, current) {
		t.Fatal("служебные поля повторной доставки изменили содержимое")
	}
	if previous.Card.Publication.Version != 3 || previous.Card.ModelKey != "derived-key" ||
		previous.Card.Publication.AttributeSearchTerms["a1"][0] != "выражение" {
		t.Fatal("сравнение изменило исходное состояние")
	}
}

func TestIdenticalProjectionContentRejectsBusinessChanges(t *testing.T) {
	tests := []struct {
		name string
		edit func(*catalog.ProductState)
	}{
		{
			name: "tenant",
			edit: func(state *catalog.ProductState) { state.TenantID = "t2" },
		},
		{
			name: "product",
			edit: func(state *catalog.ProductState) { state.ProductID = "p2" },
		},
		{
			name: "deleted",
			edit: func(state *catalog.ProductState) { state.Deleted = true },
		},
		{
			name: "stock",
			edit: func(state *catalog.ProductState) { state.Available++ },
		},
		{
			name: "price",
			edit: func(state *catalog.ProductState) { state.Price = money.FromKopecks(101) },
		},
		{
			name: "promo price",
			edit: func(state *catalog.ProductState) { state.PromoPrice = nil },
		},
		{
			name: "price profile value",
			edit: func(state *catalog.ProductState) { state.PriceProfiles[0].EffectivePrice = money.FromKopecks(89) },
		},
		{
			name: "price profile membership",
			edit: func(state *catalog.ProductState) { state.PriceProfiles = nil },
		},
		{
			name: "sales",
			edit: func(state *catalog.ProductState) { state.SalesCount++ },
		},
		{
			name: "rating",
			edit: func(state *catalog.ProductState) { state.AverageRating = 4 },
		},
		{
			name: "ratings count",
			edit: func(state *catalog.ProductState) { state.RatingsCount++ },
		},
		{
			name: "name",
			edit: func(state *catalog.ProductState) { state.Card.Name = "Другой товар" },
		},
		{
			name: "description",
			edit: func(state *catalog.ProductState) { state.Card.Description = "Другое описание" },
		},
		{
			name: "image",
			edit: func(state *catalog.ProductState) { state.Card.Image = "image-2" },
		},
		{
			name: "assets",
			edit: func(state *catalog.ProductState) { state.Card.Assets[0] = "asset-2" },
		},
		{
			name: "url",
			edit: func(state *catalog.ProductState) { state.Card.URL = "/p2" },
		},
		{
			name: "sku",
			edit: func(state *catalog.ProductState) { state.Card.SKU = "sku-2" },
		},
		{
			name: "store category",
			edit: func(state *catalog.ProductState) { state.Card.Categories[0].ID = "store-c2" },
		},
		{
			name: "visible",
			edit: func(state *catalog.ProductState) { state.Card.Visible = false },
		},
		{
			name: "only variant",
			edit: func(state *catalog.ProductState) { state.Card.OnlyVariant = true },
		},
		{
			name: "variant group",
			edit: func(state *catalog.ProductState) { state.Card.VariantsID = "group-2" },
		},
		{
			name: "variant value",
			edit: func(state *catalog.ProductState) { state.Card.Variants.Value = "v2" },
		},
		{
			name: "raw marketplace category",
			edit: func(state *catalog.ProductState) { state.Card.Marketplace.CategoryID = "c2" },
		},
		{
			name: "raw marketplace attribute",
			edit: func(state *catalog.ProductState) {
				state.Card.Marketplace.Attributes["a1"] = json.RawMessage(`"v2"`)
			},
		},
		{
			name: "raw marketplace price",
			edit: func(state *catalog.ProductState) { state.Card.Marketplace.Price = nil },
		},
		{
			name: "raw publication flag",
			edit: func(state *catalog.ProductState) { state.Card.Marketplace.Published = false },
		},
		{
			name: "publication result",
			edit: func(state *catalog.ProductState) { state.Card.Publication.Published = false },
		},
		{
			name: "publication issues",
			edit: func(state *catalog.ProductState) {
				state.Card.Publication.Issues = []taxonomy.Issue{{Code: "taxonomy.invalid"}}
			},
		},
		{
			name: "publication category",
			edit: func(state *catalog.ProductState) { state.Card.Publication.CategoryID = "c2" },
		},
		{
			name: "publication category name",
			edit: func(state *catalog.ProductState) {
				state.Card.Publication.CategoryName = "Другая категория"
			},
		},
		{
			name: "publication path",
			edit: func(state *catalog.ProductState) { state.Card.Publication.CategoryPath[0] = "c2" },
		},
		{
			name: "publication attribute",
			edit: func(state *catalog.ProductState) {
				state.Card.Publication.Attributes["a1"] = json.RawMessage(`"v2"`)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous := reviewedProjectionContent(t)
			current := reviewedProjectionContent(t)
			test.edit(&current)
			if catalog.IdenticalProjectionContent(previous, current) {
				t.Fatal("изменение содержимого признано повторной доставкой")
			}
		})
	}
}

func reviewedProjectionContent(t *testing.T) catalog.ProductState {
	t.Helper()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	promo := money.FromKopecks(90)
	marketplacePrice := json.Number("1.00")
	product, err := catalog.NewProduct(
		catalog.Snapshot{
			PriceProfiles: []catalog.PriceProfile{{
				Key:            catalog.DefaultPriceProfile,
				Price:          money.FromKopecks(100),
				EffectivePrice: money.FromKopecks(90),
			}},
			TenantID:      "t1",
			ProductID:     "p1",
			Revision:      16,
			SalesCount:    3,
			AverageRating: 3.5,
			RatingsCount:  2,
			Price:         money.FromKopecks(100),
			PromoPrice:    &promo,
			Available:     4,
			At:            now,
			Card: catalog.Card{
				ModelKey:       "derived-key",
				ModelKeySource: "derived-source",
				Variants: &catalog.Variants{
					GroupID: "group-1",
					Axis:    catalog.VariantAxis{Key: "platform.a1", Code: "a1", Name: "Атрибут"},
					Value:   "v1",
				},
				Marketplace: &taxonomy.Card{
					Price:      &marketplacePrice,
					CategoryID: "c1",
					Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`"v1"`)},
					Published:  true,
				},
				Publication: taxonomy.Publication{
					AttributeSearchTerms: map[string][]string{"a1": {"выражение"}},
					CategoryName:         "Категория",
					Version:              3,
					CategoryID:           "c1",
					CategoryPath:         []string{"c1"},
					Attributes:           map[string]json.RawMessage{"a1": json.RawMessage(`"v1"`)},
					Published:            true,
				},
				Name:        " Товар ",
				Description: "Описание",
				Image:       "image-1",
				Assets:      []string{"asset-1"},
				URL:         "/p1",
				SKU:         "sku-1",
				Categories:  []catalog.Category{{ID: "store-c1", Name: "Категория", URL: "/c1"}},
				VariantsID:  "group-1",
				Visible:     true,
			},
		},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	return product.State()
}
