package opensearch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestSearchBodyFilters(t *testing.T) {
	low, high := int64(100), int64(900)
	body := searchBody(ports.OfferQuery{
		Text:       "товар",
		TenantID:   "t1",
		CategoryID: "c1",
		Tag:        "tag-1",
		InStock:    true,
		PriceMin:   &low,
		PriceMax:   &high,
		Sort:       ports.SortPriceAsc,
		Offset:     20,
		Limit:      10,
	})

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, fragment := range []string{
		`{"term":{"listed":true}}`,
		`{"term":{"tenantId":"t1"}}`,
		`{"term":{"categoryPath":"c1"}}`,
		`{"term":{"tags":"tag-1"}}`,
		`{"term":{"inStock":true}}`,
		`{"range":{"priceProfiles.effectivePrice":{"gte":100,"lte":900}}}`,
		`"query":"товар"`,
		`"from":20`,
		`"size":10`,
		`"priceProfiles.effectivePrice":{"nested":` +
			`{"filter":{"term":{"priceProfiles.key":"marketplace:default"}},"path":"priceProfiles"},"order":"asc"}`,
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("в запросе нет %s:\n%s", fragment, text)
		}
	}
}

func TestSearchBodySorts(t *testing.T) {
	cases := map[ports.OfferSort]string{
		ports.SortPriceDesc: `"priceProfiles.effectivePrice":{"nested":` +
			`{"filter":{"term":{"priceProfiles.key":"marketplace:default"}},"path":"priceProfiles"},"order":"desc"}`,
		ports.SortNewest:    `[{"updatedAt":"desc"},{"offerId":"asc"}]`,
		ports.SortRelevance: `["_score",{"offerId":"asc"}]`,
	}

	for sort, want := range cases {
		encoded, _ := json.Marshal(searchBody(ports.OfferQuery{Text: "x", Sort: sort, Limit: 1}))
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("%s: в запросе нет %s", sort, want)
		}
	}

	encoded, _ := json.Marshal(searchBody(ports.OfferQuery{Sort: ports.SortRelevance, Limit: 1}))
	if strings.Contains(string(encoded), "multi_match") || !strings.Contains(string(encoded), `[{"updatedAt":"desc"}`) {
		t.Fatalf("без строки поиска — без полнотекста и по свежести: %s", encoded)
	}
}
