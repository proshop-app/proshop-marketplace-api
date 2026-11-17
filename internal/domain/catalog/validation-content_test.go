package catalog_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestValidationContentDependencies(t *testing.T) {
	tests := []struct {
		name string
		edit func(*catalog.ProductState)
	}{
		{name: "deleted", edit: func(p *catalog.ProductState) { p.Deleted = true }},
		{name: "name", edit: func(p *catalog.ProductState) { p.Card.Name = "Другое имя" }},
		{name: "description", edit: func(p *catalog.ProductState) { p.Card.Description = "Другой текст" }},
		{name: "image", edit: func(p *catalog.ProductState) { p.Card.Image = "image-2" }},
		{name: "marketplace presence", edit: func(p *catalog.ProductState) { p.Card.Marketplace = nil }},
		{name: "category", edit: func(p *catalog.ProductState) { p.Card.Marketplace.CategoryID = "c2" }},
		{name: "published", edit: func(p *catalog.ProductState) { p.Card.Marketplace.Published = false }},
		{name: "attribute value", edit: func(p *catalog.ProductState) {
			p.Card.Marketplace.Attributes["a1"] = json.RawMessage(`"v2"`)
		}},
		{name: "attribute removed", edit: func(p *catalog.ProductState) {
			delete(p.Card.Marketplace.Attributes, "a1")
		}},
		{name: "attribute added", edit: func(p *catalog.ProductState) {
			p.Card.Marketplace.Attributes["a2"] = json.RawMessage(`true`)
		}},
		{name: "GTIN attribute", edit: func(p *catalog.ProductState) {
			p.Card.Marketplace.Attributes["gtin"] = json.RawMessage(`"0000000000000"`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous, current := reviewedProjectionContent(t), reviewedProjectionContent(t)
			test.edit(&current)
			if catalog.SameValidationContent(previous, current) {
				t.Fatal("изменение входов проверки пропущено")
			}
		})
	}
}

func TestValidationContentIgnoresUnrelatedFields(t *testing.T) {
	tests := []struct {
		name string
		edit func(*catalog.ProductState)
	}{
		{name: "price", edit: func(p *catalog.ProductState) { p.Price = money.FromKopecks(200) }},
		{name: "promo price", edit: func(p *catalog.ProductState) { p.PromoPrice = nil }},
		{name: "price profile price", edit: func(p *catalog.ProductState) {
			p.PriceProfiles[0].Price = money.FromKopecks(200)
		}},
		{name: "price profile effective price", edit: func(p *catalog.ProductState) {
			p.PriceProfiles[0].EffectivePrice = money.FromKopecks(180)
		}},
		{name: "price profile membership", edit: func(p *catalog.ProductState) { p.PriceProfiles = nil }},
		{name: "price profile key", edit: func(p *catalog.ProductState) { p.PriceProfiles[0].Key = "marketplace:p2" }},
		{name: "marketplace price", edit: func(p *catalog.ProductState) { p.Card.Marketplace.Price = nil }},
		{name: "available", edit: func(p *catalog.ProductState) { p.Available++ }},
		{name: "sales count", edit: func(p *catalog.ProductState) { p.SalesCount++ }},
		{name: "average rating", edit: func(p *catalog.ProductState) { p.AverageRating = 4 }},
		{name: "ratings count", edit: func(p *catalog.ProductState) { p.RatingsCount++ }},
		{name: "projection revision", edit: func(p *catalog.ProductState) { p.Revision++ }},
		{name: "projection time", edit: func(p *catalog.ProductState) { p.ProjectedAt = p.ProjectedAt.Add(time.Hour) }},
		{name: "version", edit: func(p *catalog.ProductState) { p.Version++ }},
		{name: "updated at", edit: func(p *catalog.ProductState) { p.UpdatedAt = p.UpdatedAt.Add(time.Hour) }},
		{name: "assets", edit: func(p *catalog.ProductState) { p.Card.Assets[0] = "asset-2" }},
		{name: "URL", edit: func(p *catalog.ProductState) { p.Card.URL = "/p2" }},
		{name: "SKU", edit: func(p *catalog.ProductState) { p.Card.SKU = "sku-2" }},
		{name: "store categories", edit: func(p *catalog.ProductState) { p.Card.Categories = nil }},
		{name: "visible", edit: func(p *catalog.ProductState) { p.Card.Visible = false }},
		{name: "only variant", edit: func(p *catalog.ProductState) { p.Card.OnlyVariant = true }},
		{name: "variant group", edit: func(p *catalog.ProductState) { p.Card.VariantsID = "group-2" }},
		{name: "variant presence", edit: func(p *catalog.ProductState) { p.Card.Variants = nil }},
		{name: "variant position", edit: func(p *catalog.ProductState) { p.Card.Variants.Position++ }},
		{name: "variant group ID", edit: func(p *catalog.ProductState) { p.Card.Variants.GroupID = "group-2" }},
		{name: "variant axes", edit: func(p *catalog.ProductState) { p.Card.Variants.Axes[0].Value = "v2" }},
		{name: "model key", edit: func(p *catalog.ProductState) { p.Card.ModelKey = "new" }},
		{name: "model key source", edit: func(p *catalog.ProductState) { p.Card.ModelKeySource = "new" }},
		{name: "variant key", edit: func(p *catalog.ProductState) { p.Card.VariantKey = "new" }},
		{name: "derived publication", edit: func(p *catalog.ProductState) {
			p.Card.Publication = taxonomy.Publication{}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous, current := reviewedProjectionContent(t), reviewedProjectionContent(t)
			test.edit(&current)
			if !catalog.SameValidationContent(previous, current) {
				t.Fatal("поле не участвует в проверках")
			}
			if !reflect.DeepEqual(previous, reviewedProjectionContent(t)) {
				t.Fatal("сравнение изменило прежнюю карточку")
			}
		})
	}
}
