package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
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
	block := variantBlock("seller", "group", group, nil)
	if len(block.Items) != 3 || len(block.Options) != 1 {
		t.Fatalf("состав: %+v", block)
	}
	if strings.Join(block.Options[0].Values, ",") != "M,S" || block.Items[1].ID != duplicate.ID {
		t.Fatalf("порядок и сохранение всех предложений: %+v", block)
	}
	for _, item := range block.Items {
		if item.Price != 90 || item.BasePrice != 100 {
			t.Fatalf("публичная цена: %+v", item)
		}
	}
}

func TestVariantValuesFollowFirstPositionThenID(t *testing.T) {
	group := ports.OfferGroup{
		Items: []ports.OfferDocument{
			variantOffer("seller~z", "M", 2),
			variantOffer("seller~b", "S", 1),
			variantOffer("seller~later", "L", 3),
			variantOffer("seller~a", "L", 1),
		},
	}
	block := variantBlock("seller", "group", group, nil)
	if strings.Join(block.Options[0].Values, ",") != "L,S,M" {
		t.Fatalf("значения идут по первому появлению, без таксономии: %+v", block.Options)
	}
	want := []string{"seller~a", "seller~b", "seller~z", "seller~later"}
	for i, item := range block.Items {
		if item.ID != want[i] {
			t.Fatalf("position, затем id: %+v", block.Items)
		}
	}
	if group.Items[0].ID != "seller~z" {
		t.Fatal("исходная группа изменена")
	}
}

func TestVariantBlockMixedAxisSets(t *testing.T) {
	for _, legacyFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(legacyFirst), func(t *testing.T) {
			legacy := variantOffer("seller~legacy", "L", 2)
			body := `{"groupId":"group","position":2,"axis":` +
				`{"key":"platform.size","code":"size","name":"Размер"},"value":"L"}`
			if err := json.Unmarshal([]byte(body), legacy.Variants); err != nil {
				t.Fatal(err)
			}
			if legacyFirst {
				legacy.Variants.Position = 0
			}
			two := variantOffer("seller~two", "M", 1)
			color := catalog.VariantAxis{Key: "platform.color", Code: "color", Name: "Цвет", Value: "Синий"}
			two.Variants.Axes = append([]catalog.VariantAxis{color}, two.Variants.Axes...)
			reversed := variantOffer("seller~reversed", "S", 3)
			reversed.Variants.Axes = append(reversed.Variants.Axes, color)
			other := variantOffer("seller~other", "S", 4)
			other.Variants.Axes[0].Key = "platform.material"
			other.Variants.Axes[0].Code = "material"
			other.Variants.Axes = append(other.Variants.Axes, color)
			missing := variantOffer("seller~missing", "", 0)
			missing.Variants = nil
			group := ports.OfferGroup{Items: []ports.OfferDocument{reversed, legacy, other, missing, two}}
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			block := variantBlock("seller", "group", group, nil)
			wantAxes, wantItems, wantWarnings := 2, 2, 3
			if legacyFirst {
				wantAxes, wantItems, wantWarnings = 1, 1, 4
			}
			if len(block.Options) != wantAxes || len(block.Items) != wantItems {
				t.Fatalf("смешанная группа: %+v", block)
			}
			if !legacyFirst && (block.Options[0].Key != color.Key || block.Items[0].ID != two.ID) {
				t.Fatalf("оси первого подходящего предложения по позиции: %+v", block)
			}
			warnings := strings.Split(strings.TrimSpace(logs.String()), "\n")
			if len(warnings) != wantWarnings {
				t.Fatal(logs.String())
			}
			for _, warning := range warnings {
				var record map[string]string
				if err := json.Unmarshal([]byte(warning), &record); err != nil {
					t.Fatal(err)
				}
				if record["level"] != "WARN" || record["tenantId"] != "seller" ||
					record["groupId"] != "group" || record["offer"] == "" {
					t.Fatal(record)
				}
			}
		})
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
		block := variantBlock("seller", "group", group, nil)
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
