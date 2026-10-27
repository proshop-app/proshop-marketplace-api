package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	pricemoney "github.com/proshop-app/proshop-marketplace/pkg/money"
)

const testTaxonomyYAML = `version: 1
categories:
  - id: c1
    name: Категория
    attributes:
      - code: a1
        name: Атрибут
        type: enum
        values: [v1, v2]
        required: true
        facet: true
`

func TestTaxonomyHTTP(t *testing.T) {
	store := fake.NewStore()
	clock := fake.NewClock(time.Now())
	index := fake.NewIndex()
	handler := NewHandler(Deps{
		Registerer:    prometheus.NewRegistry(),
		Taxonomy:      apptaxonomy.NewService(store, clock),
		Catalog:       appcatalog.NewQueries(store, index, fake.NewPlatform()),
		OperatorToken: "token",
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	call := func(method, path, body, token, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call(http.MethodPut, "/api/v1/operator/taxonomy", testTaxonomyYAML, "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call(http.MethodPut, "/api/v1/operator/taxonomy", testTaxonomyYAML, "token", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{
		"/api/v1/taxonomy/categories",
		"/api/v1/taxonomy/categories/c1/schema",
		"/api/v1/taxonomy/version",
	} {
		w := call(http.MethodGet, path, "", "", "")
		if w.Code != 200 || w.Header().Get("ETag") != `"taxonomy-1"` || w.Header().Get("X-Taxonomy-Version") != "1" {
			t.Fatal(w.Code, w.Header(), w.Body.String())
		}
		w = call(http.MethodGet, path, "", "", `"other", W/"taxonomy-1"`)
		if w.Code != 304 || w.Body.Len() != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := call(http.MethodGet, "/api/v1/taxonomy/categories/missing/schema", "", "", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := call(http.MethodGet, "/api/v1/operator/taxonomy", "", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call(http.MethodGet, "/api/v1/operator/taxonomy", "", "token", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := call(http.MethodPut, "/api/v1/operator/taxonomy", testTaxonomyYAML, "token", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	changedYAML := strings.ReplaceAll(testTaxonomyYAML, "v2", "v3")
	w := call(http.MethodPut, "/api/v1/operator/taxonomy", changedYAML, "token", "")
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(http.MethodPut, "/api/v1/operator/taxonomy", testTaxonomyYAML+"unknown: true\n", "token", "")
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := appcatalog.NewProjector(store, clock).ApplyProduct(context.Background(), appcatalog.ProductEvent{
		EventID: "e1",
		Snapshot: catalog.Snapshot{
			TenantID:  "t1",
			ProductID: "p1",
			Revision:  1,
			At:        clock.Now(),
			Card:      catalog.Card{Name: "Товар"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	w = call(http.MethodGet, "/api/v1/operator/catalog/offers/t1~p1/publication", "", "token", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "taxonomy.card_missing") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(http.MethodGet, "/api/v1/operator/catalog/offers/t1~p1/publication", "", "", "")
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	if err := index.Put(context.Background(), []ports.OfferDocument{
		{
			ID:           "t1~p1",
			TenantID:     "t1",
			ProductID:    "p1",
			CategoryID:   "c1",
			CategoryPath: []string{"c1"},
			Attributes:   map[string]json.RawMessage{"a1": json.RawMessage(`"v1"`)},
			Listed:       true,
			Visible:      true,
			Version:      1,
			PriceProfiles: []catalog.PriceProfile{
				{
					Key:            catalog.DefaultPriceProfile,
					Price:          pricemoney.FromKopecks(0),
					EffectivePrice: pricemoney.FromKopecks(0),
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	w = call(http.MethodGet, "/api/v1/catalog/offers?category=c1&attr.a1=v1", "", "", "")
	var page offerPageJSON
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || page.Total != 1 || len(page.Facets) != 5 || string(page.Items[0].Attributes["a1"]) != `"v1"` {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodGet, "/api/v1/catalog/offers?attr.a1=v3", "", "", ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestAttributeQueryParsing(t *testing.T) {
	r := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/catalog/offers?attr.a1=v1&attr.a1=v2&attr.a2.min=1.5&attr.a2.max=3",
		nil,
	)
	q, err := parseOfferQuery(r)
	if err != nil || len(q.Attributes["a1"].Values) != 2 || q.Attributes["a2"].Min != "1.5" {
		t.Fatalf("%+v %v", q, err)
	}
}
