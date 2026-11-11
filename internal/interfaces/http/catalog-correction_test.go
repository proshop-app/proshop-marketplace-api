package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestCatalogCorrectionHTTP(t *testing.T) {
	for _, kind := range []string{"", "typo", "layout", "translit", "partial"} {
		t.Run(kind, func(t *testing.T) {
			store := fake.NewStore()
			store.SeedTaxonomy(taxonomy.Definition{})
			index := &fake.PromotionIndex{
				Index: fake.NewIndex(),
				Result: ports.OfferPage{
					Total: 1,
					Items: []ports.OfferDocument{{
						ID:        "t1~p1",
						Price:     19999,
						BasePrice: 29999,
					}},
				},
			}
			if kind != "" {
				index.Result.Correction = &ports.OfferCorrection{
					Query: "товар",
					Kind:  kind,
				}
			}
			handler := catalogHandlers{queries: appcatalog.NewQueries(
				store,
				index,
				fake.NewPlatform(),
				nil,
				nil,
				fake.NewClock(time.Time{}),
			)}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/offers?q=test&facets=none", nil)
			response := httptest.NewRecorder()
			handler.list(response, request)
			if response.Code != http.StatusOK {
				t.Fatal(response.Code, response.Body.String())
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if string(body["facets"]) != "[]" || string(body["total"]) != "1" ||
				!strings.Contains(response.Body.String(), `"price":199.99`) ||
				!strings.Contains(response.Body.String(), `"basePrice":299.99`) {
				t.Fatal("изменён контракт выдачи", response.Body.String())
			}
			if kind == "" {
				if _, found := body["correction"]; found {
					t.Fatal("обычная выдача содержит correction", response.Body.String())
				}
				return
			}
			var correction offerCorrectionJSON
			if err := json.Unmarshal(body["correction"], &correction); err != nil {
				t.Fatal(err)
			}
			if correction.Query != "товар" || correction.Kind != kind {
				t.Fatal("потеряно исправление поиска", correction)
			}
		})
	}
}
