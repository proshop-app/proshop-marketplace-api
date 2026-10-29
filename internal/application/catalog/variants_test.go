package catalog

import (
	"fmt"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func variantOffer(id, value string, position int) ports.OfferDocument {
	return ports.OfferDocument{
		ID:       id,
		TenantID: "seller",
		Visible:  true,
		Variants: &catalog.Variants{
			GroupID: "group",
			Axis: catalog.VariantAxis{
				Key:  "platform.size",
				Code: "size",
				Name: "Размер",
			},
			Value:    value,
			Position: position,
		},
		PriceProfiles: []catalog.PriceProfile{
			{
				Key:            "marketplace:default",
				Price:          money.FromKopecks(100),
				EffectivePrice: money.FromKopecks(90),
			},
			{
				Key:            "marketplace:member",
				Price:          money.FromKopecks(100),
				EffectivePrice: money.FromKopecks(80),
			},
		},
	}
}

func TestVariantBlockScopePriceOrderAndDuplicates(t *testing.T) {
	current := variantOffer("seller~m", "M", 1)
	small := variantOffer("seller~s", "S", 9)
	duplicate := variantOffer("seller~m2", "M", 2)
	foreign := variantOffer("other~s", "S", 0)
	foreign.TenantID = "other"
	hidden := variantOffer("seller~hidden", "S", 0)
	hidden.Visible = false
	wrongAxis := variantOffer("seller~color", "Red", 0)
	wrongAxis.Variants.Axis.Code = "color"
	noPrice := variantOffer("seller~noprice", "L", 3)
	noPrice.PriceProfiles = nil
	group := ports.OfferGroup{
		Items: []ports.OfferDocument{duplicate, current, foreign, hidden, wrongAxis, noPrice, small},
	}
	block := variantBlock(current, group, "marketplace:member", []string{"S", "M", "L"})
	if block == nil || len(block.Items) != 3 {
		t.Fatalf("состав: %+v", block)
	}
	if block.Items[0].Value != "S" || !block.Items[1].Current || !block.Items[2].Duplicate {
		t.Fatalf("порядок и дубли: %+v", block.Items)
	}
	for _, item := range block.Items {
		if item.Price != 80 || item.BasePrice != 100 {
			t.Fatalf("ценовой контекст: %+v", item)
		}
	}
}

func TestVariantBlockTruncatedIncludesCurrentWithinLimit(t *testing.T) {
	group := ports.OfferGroup{Truncated: true}
	for i := range 200 {
		group.Items = append(group.Items, variantOffer(fmt.Sprintf("seller~%d", i), fmt.Sprint(i), i))
	}
	current := variantOffer("seller~last", "last", 205)
	block := variantBlock(current, group, "marketplace:default", nil)
	if block == nil || len(block.Items) != 200 || !block.Truncated || !block.Items[199].Current {
		t.Fatalf("обрезанная группа: %+v", block)
	}
}
