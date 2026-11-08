package http

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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

func facetSelectionHandler(t *testing.T) catalogHandlers {
	t.Helper()
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
	return catalogHandlers{
		queries: appcatalog.NewQueries(store, index, nil, nil, nil, fake.NewClock(time.Time{})),
	}
}

func TestCatalogFacetSelectionHTTP(t *testing.T) {
	h := facetSelectionHandler(t)
	for _, tc := range []struct {
		query    string
		codes    []string
		selector string
		class    string
		empty    bool
	}{
		{
			query:    "?limit=5",
			codes:    []string{"category", "tenant", "price", "inStock", "attr.a1"},
			selector: "all",
			class:    "all",
		},
		{
			query:    "?limit=100",
			codes:    []string{"category", "tenant", "price", "inStock", "attr.a1"},
			selector: "all",
			class:    "all",
		},
		{
			query:    "?facets=category&q=missing",
			codes:    []string{"category"},
			selector: "category",
			class:    "partial",
			empty:    true,
		},
		{
			query:    "?facets=none",
			codes:    []string{},
			selector: "none",
			class:    "none",
		},
		{
			query:    "?facets=category&facets=tenant,attributes",
			codes:    []string{"category", "tenant", "attr.a1"},
			selector: "category,tenant,attributes",
			class:    "partial",
		},
		{
			query:    "?facets=attributes",
			codes:    []string{"attr.a1"},
			selector: "attributes",
			class:    "partial",
		},
		{
			query:    "?facets=category&attributeFacets=true",
			codes:    []string{"category", "attr.a1"},
			selector: "category,attributes",
			class:    "partial",
		},
		{
			query:    "?facets=category,tenant,inStock,price,attributes",
			codes:    []string{"category", "tenant", "price", "inStock", "attr.a1"},
			selector: "category,tenant,inStock,price,attributes",
			class:    "all",
		},
		{
			query:    "?facets=&attributeFacets=false",
			codes:    []string{},
			selector: "none",
			class:    "none",
		},
		{
			query:    "?facets=",
			codes:    []string{"attr.a1"},
			selector: "attributes",
			class:    "partial",
		},
		{
			query:    "?attributeFacets=false",
			codes:    []string{"category", "tenant", "price", "inStock"},
			selector: "category,tenant,inStock,price",
			class:    "partial",
		},
		{
			query:    "?attributeFacets=true",
			codes:    []string{"category", "tenant", "price", "inStock", "attr.a1"},
			selector: "all",
			class:    "all",
		},
		{
			query:    "?facets=category,tenant&attributeFacets=false",
			codes:    []string{"category", "tenant"},
			selector: "category,tenant",
			class:    "partial",
		},
		{
			query:    "?facets=category",
			codes:    []string{"category"},
			selector: "category",
			class:    "partial",
		},
	} {
		t.Run(tc.query, func(t *testing.T) {
			var logs bytes.Buffer
			registry := prometheus.NewRegistry()
			handler := observe(
				slog.New(slog.NewJSONHandler(&logs, nil)),
				newHTTPMetrics(registry),
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
			wantTotal := 1
			if tc.empty {
				wantTotal = 0
			}
			if !reflect.DeepEqual(codes, tc.codes) || page.Total != wantTotal || len(page.Items) != wantTotal {
				t.Fatalf("неверная выдача/выбор фасетов: %s", response.Body.String())
			}
			var record struct {
				Selector   string `json:"catalogFacetSelector"`
				Total      *int   `json:"catalogFacetCount"`
				Attributes *int   `json:"catalogAttributeFacetCount"`
			}
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if record.Selector != tc.selector {
				t.Fatalf("неверный селектор в журнале: %s", logs.String())
			}
			assertCatalogOfferCounters(t, registry, tc.class)
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
		"?facets=category&facets=category",
		"?facets=category,none",
		"?facets=none&facets=tenant",
		"?facets=none&attributeFacets=true",
		"?facets=attributes&attributeFacets=false",
		"?facets=attributes,attributes",
		"?facets=&facets=category",
		"?facets=category,%20tenant",
		"?attributeFacets=1",
		"?attributeFacets=FALSE",
		"?attributeFacets=",
		"?attributeFacets=false&attributeFacets=true",
	} {
		response := httptest.NewRecorder()
		var logs bytes.Buffer
		registry := prometheus.NewRegistry()
		handler := observe(
			slog.New(slog.NewJSONHandler(&logs, nil)),
			newHTTPMetrics(registry),
			http.HandlerFunc(h.list),
		)
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/offers"+query, nil))
		assertCatalogOfferCounters(t, registry, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d, %s", query, response.Code, response.Body.String())
		}
	}
}

func assertCatalogOfferCounters(t *testing.T, registry *prometheus.Registry, selected string) {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, family := range families {
		if family.GetName() != "marketplace_catalog_offers_total" {
			continue
		}
		for _, metric := range family.Metric {
			want := float64(0)
			if metric.Label[0].GetValue() == selected {
				want = 1
			}
			if metric.GetCounter().GetValue() != want {
				t.Fatalf("неверный счётчик %s: %v", metric.Label[0].GetValue(), metric.GetCounter().GetValue())
			}
			found++
		}
	}
	if found != 3 {
		t.Fatalf("ожидались только три класса селектора, получено %d", found)
	}
}

func TestCatalogFacetLegacyResponseBytes(t *testing.T) {
	// Фикстура снята тем же fake-сценарием с master 5ae2d7a, до изменения селектора.
	want, err := os.ReadFile("testdata/catalog-offers-legacy-5ae2d7a.json")
	if err != nil {
		t.Fatal(err)
	}
	h := facetSelectionHandler(t)
	response := httptest.NewRecorder()
	h.list(response, httptest.NewRequest(http.MethodGet, "/offers?limit=5", nil))
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), want) {
		t.Fatalf("ответ без селектора изменился: %s", response.Body.String())
	}
}
