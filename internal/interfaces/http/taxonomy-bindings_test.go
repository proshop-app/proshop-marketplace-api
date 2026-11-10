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

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func hintHTTP(t *testing.T) (*fake.Store, http.Handler) {
	t.Helper()
	store := fake.NewStore()
	store.SeedTaxonomy(taxonomy.Definition{
		Version:    1,
		Attributes: []taxonomy.Attribute{{Code: "a1", Name: "Поле", Type: "string", Dictionary: "a1"}},
		Schemas:    map[string]taxonomy.Schema{"s1": {Attributes: []string{"a1"}}},
		Categories: []taxonomy.Category{{ID: "c1", Name: "Категория", Slug: "c1", Schema: "s1"}},
	})
	for _, code := range []string{"a1", "d1", "d2"} {
		store.SeedReferenceDictionary(reference.DictionaryState{Code: code, Kind: reference.KindOpen})
	}
	return store, NewHandler(Deps{
		Registerer:    prometheus.NewRegistry(),
		Taxonomy:      apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil),
		OperatorToken: "token",
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func bindingHTTPCall(handler http.Handler, method, rawQuery, body, match, token string) *httptest.ResponseRecorder {
	path := "/api/v1/operator/taxonomy/dictionary-bindings/a1"
	if method == http.MethodGet {
		path = "/api/v1/taxonomy/dictionary-bindings"
	}
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.URL.RawQuery = rawQuery
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Marketplace-Money-Unit", "RUB")
	request.Header.Set("If-Match", match)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestDictionaryBindingsHTTPUsesVersionAndNarrowCASRules(t *testing.T) {
	_, handler := hintHTTP(t)
	response := bindingHTTPCall(handler, "GET", "", "", "", "")
	var first dictionaryBindingsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &first); err != nil || response.Code != 200 ||
		first.Version != 1 || first.DictionaryBindings == nil || len(first.DictionaryBindings) != 0 {
		t.Fatal("пустой public part не является массивом", response.Body.String(), err)
	}
	body := `{"mode":"extend","dictionaries":["d1","d2"]}`
	if r := bindingHTTPCall(handler, "PUT", "categoryId=c1", body, `"taxonomy-1"`, ""); r.Code != 401 {
		t.Fatal("неавторизованная запись", r.Code)
	}
	response = bindingHTTPCall(handler, "PUT", "categoryId=c1", body, `"taxonomy-1"`, "token")
	if response.Code != 200 || response.Header().Get("ETag") != `"taxonomy-2"` {
		t.Fatal(response.Code, response.Body.String())
	}
	response = bindingHTTPCall(
		handler,
		"PUT",
		"categoryId=c1",
		`{"mode":"extend","dictionaries":["d2","d1"]}`,
		`"taxonomy-2"`,
		"token")
	if response.Code != 200 || response.Header().Get("ETag") != `"taxonomy-2"` {
		t.Fatal("перестановка не является no-op", response.Code, response.Body.String())
	}
	if r := bindingHTTPCall(handler, "DELETE", "categoryId=c1", "", `"taxonomy-1"`, "token"); r.Code != 409 {
		t.Fatal("stale delete принят", r.Code, r.Body.String())
	}
	response = bindingHTTPCall(handler, "DELETE", "categoryId=c1", "", `"taxonomy-2"`, "token")
	if response.Code != 200 || response.Header().Get("ETag") != `"taxonomy-3"` {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestDictionaryBindingMalformedQueryCannotWidenToGlobalAndStrictBody(t *testing.T) {
	for _, query := range []string{
		"categoryId=%XX",
		"categoryId=c1;bad",
		"categoryId=",
		"categoryId=c1&categoryId=c1",
		"extra=1",
	} {
		store, handler := hintHTTP(t)
		response := bindingHTTPCall(
			handler,
			"PUT",
			query,
			`{"mode":"replace","dictionaries":["d1"]}`,
			`"taxonomy-1"`,
			"token")
		if response.Code != 400 {
			t.Fatal("неверный query принят", query, response.Code, response.Body.String())
		}
		current, _ := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil).Current(context.Background())
		if current.Version != 1 || len(current.DictionaryBindings) != 0 {
			t.Fatal("query превратился в глобальную запись", query, current)
		}
	}
	for _, body := range []string{
		`null`,
		`[]`,
		`{}`,
		`{"mode":"replace"}`,
		`{"mode":"replace","dictionaries":null}`,
		`{"mode":null,"dictionaries":[]}`,
		`{"mode":"extend","dictionaries":[]}`,
		`{"mode":"replace","dictionaries":[],"attributeCode":"forged"}`,
		`{"mode":"replace","dictionaries":[]} {}`,
	} {
		_, handler := hintHTTP(t)
		response := bindingHTTPCall(handler, "PUT", "", body, `"taxonomy-1"`, "token")
		if response.Code != 400 {
			t.Fatal("нестрогое тело принято", body, response.Code, response.Body.String())
		}
	}
}
