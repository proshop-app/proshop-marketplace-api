package http

import (
	"encoding/json"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestOfferAssetsOnlyInCard(t *testing.T) {
	for _, assets := range [][]string{nil, {"main", "detail"}} {
		card := appcatalog.OfferCard{Offer: ports.OfferDocument{Assets: assets}}
		for _, detail := range []bool{true, false} {
			raw, err := json.Marshal(toOfferJSON(card, detail))
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			got, exists := fields["assets"]
			if exists != detail {
				t.Fatalf("карточка=%v: %s", detail, raw)
			}
			if detail {
				want := `[]`
				if len(assets) > 0 {
					want = `["main","detail"]`
				}
				if string(got) != want {
					t.Fatalf("assets=%s, ожидалось %s", got, want)
				}
			}
		}
	}
}

func TestOpenAPIOfferAssets(t *testing.T) {
	doc, _ := loadSpec(t)
	offer := doc.Components["schemas"]["Offer"].(map[string]any)
	properties := offer["properties"].(map[string]any)
	assets := properties["assets"].(map[string]any)
	items := assets["items"].(map[string]any)
	if assets["type"] != "array" || assets["maxItems"] != 20 || assets["uniqueItems"] != true {
		t.Fatal(assets)
	}
	if items["type"] != "string" || items["maxLength"] != 512 {
		t.Fatal(items)
	}
}
