package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
)

func TestCatalogExactHTTP(t *testing.T) {
	for _, tc := range []struct {
		name   string
		suffix string
		exact  bool
	}{
		{
			name: "без параметра",
		},
		{
			name:   "false",
			suffix: "&exact=false",
		},
		{
			name:   "true",
			suffix: "&exact=true",
			exact:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index := &fake.PromotionIndex{}
			handler := catalogHandlers{queries: appcatalog.NewQueries(
				fake.NewStore(),
				index,
				fake.NewPlatform(),
				nil,
				nil,
				fake.NewClock(time.Time{}),
			)}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/offers?q=test"+tc.suffix, nil)
			response := httptest.NewRecorder()
			handler.list(response, request)

			if response.Code != http.StatusOK {
				t.Fatal(response.Code, response.Body.String())
			}
			if index.Query.Exact != tc.exact || index.Query.Text != "test" {
				t.Fatalf("параметры не дошли до порта поиска: %+v", index.Query)
			}
		})
	}
}

func TestCatalogExactHTTPRejectsInvalid(t *testing.T) {
	for _, raw := range []string{
		"",
		"bad",
		"1",
		"0",
		"TRUE",
		"FALSE",
		"True",
		"%20true",
		"true%20",
		"true&exact=false",
		"true&exact=true",
		"false&exact=false",
	} {
		t.Run(raw, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/offers?exact="+raw, nil)
			response := httptest.NewRecorder()
			handler := catalogHandlers{}
			handler.list(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatal(response.Code, response.Body.String())
			}
			var body errorBody
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != "catalog.invalid_query" || body.Error.Message != "exact — true или false" {
				t.Fatal("нарушен формат ошибки", response.Body.String())
			}
		})
	}
}

func TestSharedOfferQueryIgnoresCatalogExact(t *testing.T) {
	for _, raw := range []string{"true", "false", "bad", ""} {
		request := httptest.NewRequest(http.MethodGet, "/?q=test&exact="+raw, nil)
		query, err := parseOfferQuery(request)
		if err != nil || query.Exact || query.Text != "test" {
			t.Fatalf("изменён общий разбор для рекламного подбора: %+v, %v", query, err)
		}
	}
}
