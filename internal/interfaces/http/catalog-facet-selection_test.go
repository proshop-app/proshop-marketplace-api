package http

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestCatalogFacetSelectionHTTP(t *testing.T) {
	store, index := fake.NewStore(), fake.NewIndex()
	store.SeedTaxonomy(taxonomy.Definition{
		Attributes: []taxonomy.Attribute{{
			Code:  "a1",
			Type:  "enum",
			Facet: true,
		}},
	})
	if err := index.Put(context.Background(), []ports.OfferDocument{{
		ID:         "t1~p1",
		TenantID:   "t1",
		CategoryID: "c1",
		Visible:    true,
		Listed:     true,
		Version:    1,
		Attributes: map[string]json.RawMessage{
			"a1": json.RawMessage(`"v1"`),
		},
		PriceProfiles: []catalog.PriceProfile{{
			Key:            catalog.DefaultPriceProfile,
			Price:          money.FromKopecks(100),
			EffectivePrice: money.FromKopecks(100),
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	h := catalogHandlers{
		queries: appcatalog.NewQueries(store, index, nil, nil, nil, fake.NewClock(time.Time{})),
	}
	for _, tc := range []struct {
		query string
		codes []string
	}{
		{
			query: "?limit=5",
			codes: []string{"category", "tenant", "price", "inStock", "attr.a1"},
		},
		{
			query: "?limit=100",
			codes: []string{"category", "tenant", "price", "inStock", "attr.a1"},
		},
		{
			query: "?facets=&attributeFacets=false",
			codes: []string{},
		},
		{
			query: "?facets=",
			codes: []string{"attr.a1"},
		},
		{
			query: "?attributeFacets=false",
			codes: []string{"category", "tenant", "price", "inStock"},
		},
		{
			query: "?attributeFacets=true",
			codes: []string{"category", "tenant", "price", "inStock", "attr.a1"},
		},
		{
			query: "?facets=category,tenant&attributeFacets=false",
			codes: []string{"category", "tenant"},
		},
		{
			query: "?facets=category",
			codes: []string{"category", "attr.a1"},
		},
	} {
		t.Run(tc.query, func(t *testing.T) {
			var logs bytes.Buffer
			handler := observe(
				slog.New(slog.NewJSONHandler(&logs, nil)),
				newHTTPMetrics(prometheus.NewRegistry()),
				http.HandlerFunc(h.list),
			)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/offers"+tc.query, nil))
			if response.Code != http.StatusOK {
				t.Fatal(response.Code, response.Body.String())
			}
			var page offerPageJSON
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			codes := []string{}
			for _, facet := range page.Facets {
				codes = append(codes, facet.Code)
			}
			if !reflect.DeepEqual(codes, tc.codes) || page.Total != 1 || len(page.Items) != 1 {
				t.Fatalf("неверная выдача/выбор фасетов: %s", response.Body.String())
			}
			var record struct {
				Total      *int `json:"catalogFacetCount"`
				Attributes *int `json:"catalogAttributeFacetCount"`
			}
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			attributes := 0
			for _, code := range codes {
				if strings.HasPrefix(code, "attr.") {
					attributes++
				}
			}
			if record.Total == nil || record.Attributes == nil ||
				*record.Total != len(codes) || *record.Attributes != attributes {
				t.Fatalf("журнал не отражает фактические фасеты ответа: %s", logs.String())
			}
		})
	}
	for _, query := range []string{
		"?facets=missing",
		"?facets=category,category",
		"?facets=category,",
		"?facets=category&facets=tenant",
		"?facets=category,%20tenant",
		"?attributeFacets=1",
		"?attributeFacets=FALSE",
		"?attributeFacets=",
		"?attributeFacets=false&attributeFacets=true",
	} {
		response := httptest.NewRecorder()
		h.list(response, httptest.NewRequest(http.MethodGet, "/offers"+query, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d, %s", query, response.Code, response.Body.String())
		}
	}
}
