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
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	pricemoney "github.com/proshop-app/proshop-marketplace/pkg/money"
)

const testTaxonomyYAML = `- id: c1
  name: Категория
  title: Полное имя категории
  slug: c1
  schema: s1
`

func TestTaxonomyHTTP(t *testing.T) {
	store := fake.NewStore()
	store.SeedTaxonomy(taxonomy.Definition{
		Version: 1,
		Attributes: []taxonomy.Attribute{
			{
				Code:       "a1",
				Name:       "Атрибут",
				Type:       "enum",
				Dictionary: "a1",
				Values:     []string{"v1", "v2"},
				Facet:      true,
			},
		},
		Schemas: map[string]taxonomy.Schema{"s1": {Attributes: []string{"a1"}}},
		Categories: []taxonomy.Category{
			{
				ID:     "c1",
				Name:   "Категория",
				Title:  "Полное имя категории",
				Slug:   "c1",
				Schema: "s1",
			},
		},
	})
	seedHTTPDictionary(t, store)
	clock := fake.NewClock(time.Now())
	index := fake.NewIndex()
	handler := NewHandler(Deps{
		Registerer:    prometheus.NewRegistry(),
		Taxonomy:      apptaxonomy.NewService(store, clock, nil),
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
			if method == http.MethodPut {
				r.Header.Set("If-Match", etag)
			} else {
				r.Header.Set("If-None-Match", etag)
			}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call(http.MethodPut, "/api/v1/operator/taxonomy/categories", testTaxonomyYAML, "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, path := range []string{
		"/api/v1/taxonomy/categories",
		"/api/v1/taxonomy/categories/c1/schema",
		"/api/v1/taxonomy/version",
		"/api/v1/taxonomy/attributes",
		"/api/v1/taxonomy/schemas",
	} {
		w := call(http.MethodGet, path, "", "", "")
		if w.Code != 200 || w.Header().Get("ETag") != `"taxonomy-1"` || w.Header().Get("X-Taxonomy-Version") != "1" {
			t.Fatal(w.Code, w.Header(), w.Body.String())
		}
		if strings.Contains(path, "/categories") &&
			(!strings.Contains(w.Body.String(), `"slug":"c1"`) ||
				!strings.Contains(w.Body.String(), `"title":"Полное имя категории"`)) {
			t.Fatalf("нет адреса или полного имени в API: %s", w.Body.String())
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
	for _, part := range []string{"categories", "attributes", "schemas"} {
		path := "/api/v1/operator/taxonomy/" + part
		w := call(http.MethodGet, path, "", "token", "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		etag := w.Header().Get("ETag")
		downloaded := w.Body.String()
		w = call(http.MethodPut, path, downloaded, "token", etag)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if stale := call(http.MethodPut, path, downloaded, "token", etag); stale.Code != 409 {
			t.Fatal(stale.Code, stale.Body.String())
		}
	}
	w := call(http.MethodPut, "/api/v1/operator/taxonomy/categories", testTaxonomyYAML, "token", "")
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	w = call(
		http.MethodPut,
		"/api/v1/operator/taxonomy/categories",
		testTaxonomyYAML+"  unknown: true\n",
		"token",
		`"taxonomy-4"`,
	)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if old := call(http.MethodPut, "/api/v1/operator/taxonomy", testTaxonomyYAML, "token", ""); old.Code != 404 {
		t.Fatal(old.Code)
	}
	if err := appcatalog.NewProjector(store, clock, nil).ApplyProduct(context.Background(), appcatalog.ProductEvent{
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

func seedHTTPDictionary(t *testing.T, store *fake.Store) {
	t.Helper()
	state := reference.DictionaryState{
		Code:   "a1",
		Kind:   reference.KindClosed,
		Limits: reference.DefaultLimits(),
	}
	store.SeedReferenceDictionary(state)
	err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		entries := []*reference.Entry{}
		generator := &fake.IDs{}
		for _, value := range []string{"v1", "v2"} {
			entry, err := reference.NewEntry(
				reference.RestoreDictionary(state),
				reference.NewEntryInput{
					ID:               generator.NewUUID(),
					CanonicalAliasID: generator.NewUUID(),
					Name:             value,
					Status:           reference.StatusConfirmed,
					Source:           reference.SourceSeed,
				},
				time.Now(),
			)
			if err != nil {
				return err
			}
			entry.Bump(time.Now())
			entries = append(entries, entry)
		}
		return tx.Reference().Save(ctx, entries, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
}
