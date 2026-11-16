package catalog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func variantOffer(id, value string, position int) ports.OfferDocument {
	return ports.OfferDocument{
		ID:         id,
		TenantID:   "seller",
		VariantsID: "group",
		Visible:    true,
		Price:      90,
		BasePrice:  100,
		Variants: &catalog.Variants{
			GroupID: "group",
			Axes: []catalog.VariantAxis{
				{
					Key:   "platform.size",
					Code:  "size",
					Name:  "Размер",
					Value: value,
				},
			},
			Position: position,
		},
	}
}

func TestVariantBlockScopeOrderAndDuplicates(t *testing.T) {
	current := variantOffer("seller~m", "M", 1)
	small := variantOffer("seller~s", "S", 9)
	duplicate := variantOffer("seller~m2", "M", 2)
	foreign := variantOffer("other~s", "S", 0)
	foreign.TenantID = "other"
	hidden := variantOffer("seller~hidden", "S", 0)
	hidden.Visible = false
	noAxes := variantOffer("seller~noaxes", "", 0)
	noAxes.Variants = nil
	group := ports.OfferGroup{
		Items: []ports.OfferDocument{duplicate, current, foreign, hidden, small, noAxes},
	}
	block := variantBlock("seller", "group", group, taxonomy.Definition{})
	if len(block.Items) != 4 || len(block.Options) != 1 {
		t.Fatalf("состав: %+v", block)
	}
	if strings.Join(block.Options[0].Values, ",") != "M,S" || block.Items[2].ID != duplicate.ID {
		t.Fatalf("порядок и сохранение всех предложений: %+v", block)
	}
	for _, item := range block.Items {
		if item.Price != 90 || item.BasePrice != 100 {
			t.Fatalf("ценовой контекст: %+v", item)
		}
	}
}

func TestVariantBlockAllItemsMultipleAxesAndOrderedJSON(t *testing.T) {
	for _, count := range []int{1, 2, 3} {
		group := ports.OfferGroup{Truncated: true}
		for i := range 200 {
			offer := variantOffer(fmt.Sprintf("seller~%d", i), fmt.Sprint(i), i)
			for axis := 1; axis < count; axis++ {
				code := fmt.Sprintf("axis%d", axis)
				offer.Variants.Axes = append(offer.Variants.Axes, catalog.VariantAxis{
					Key:   "platform." + code,
					Code:  code,
					Name:  code,
					Value: fmt.Sprint(i % 2),
				})
			}
			group.Items = append(group.Items, offer)
		}
		block := variantBlock("seller", "group", group, taxonomy.Definition{})
		if len(block.Items) != 200 || len(block.Options) != count || !block.Truncated {
			t.Fatalf("обрезанная группа: %+v", block)
		}
		body, err := json.Marshal(block)
		if err != nil || strings.Contains(string(body), "duplicate") {
			t.Fatalf("JSON: %s %v", body, err)
		}
		var decoded struct {
			Options []VariantOption `json:"options"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil || len(decoded.Options) != count {
			t.Fatalf("массив осей: %+v %v", decoded, err)
		}
		for i, option := range decoded.Options {
			if option.Key != group.Items[0].Variants.Axes[i].Key {
				t.Fatal("порядок осей потерян")
			}
		}
	}
}
