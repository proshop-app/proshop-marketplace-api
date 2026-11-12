package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func sourcesHTTPCall(handler http.Handler, etag string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/taxonomy/dictionary-sources", nil)
	request.Header.Set("If-None-Match", etag)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestDictionarySourcesHTTPSharedFixtureAndCacheHeaders(t *testing.T) {
	store, handler := hintHTTP(t)
	raw, err := os.ReadFile("../../../docs/api/examples/dictionary-bindings-resolution.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Definition taxonomy.Definition `json:"definition"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	store.SeedTaxonomy(fixture.Definition)

	raw, err = os.ReadFile("../../../docs/api/examples/dictionary-sources.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected, actual dictionarySourcesResponse
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	response := sourcesHTTPCall(handler, "")
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !reflect.DeepEqual(actual, expected) {
		t.Fatal(response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		response.Header().Get("ETag") != `"taxonomy-1"` || response.Header().Get("X-Taxonomy-Version") != "1" ||
		response.Header().Get("Cache-Control") != "public, max-age=0, must-revalidate" {
		t.Fatal(response.Header())
	}
	for _, etag := range []string{`"taxonomy-1"`, `"other", W/"taxonomy-1"`, "*"} {
		response = sourcesHTTPCall(handler, etag)
		if response.Code != http.StatusNotModified || response.Body.Len() != 0 ||
			response.Header().Get("ETag") != `"taxonomy-1"` || response.Header().Get("X-Taxonomy-Version") != "1" {
			t.Fatal(etag, response.Code, response.Header(), response.Body.String())
		}
	}

	fixture.Definition.Version++
	fixture.Definition.DictionaryBindings = nil
	store.SeedTaxonomy(fixture.Definition)
	response = sourcesHTTPCall(handler, `"taxonomy-1"`)
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || actual.Version != 2 || response.Header().Get("ETag") != `"taxonomy-2"` ||
		response.Header().Get("X-Taxonomy-Version") != "2" ||
		!reflect.DeepEqual(actual.DictionarySources, fixture.Definition.DictionarySources()) {
		t.Fatal("кэш не обновился вместе со снимком", response.Code, response.Header(), actual)
	}
}

func TestDictionarySourcesHTTPEmptyObject(t *testing.T) {
	definitions := []taxonomy.Definition{
		{},
		{Version: 1},
		{
			Version:    2,
			Attributes: []taxonomy.Attribute{{Code: "a1", Type: "string"}},
			Schemas:    map[string]taxonomy.Schema{"s1": {Attributes: []string{"a1"}}},
			Categories: []taxonomy.Category{{ID: "c1", Schema: "s1"}},
		},
	}
	for _, d := range definitions {
		store, handler := hintHTTP(t)
		store.SeedTaxonomy(d)
		response := sourcesHTTPCall(handler, "")
		var actual dictionarySourcesResponse
		if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || actual.Version != d.Version || actual.DictionarySources == nil ||
			len(actual.DictionarySources) != 0 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
}

func TestDictionarySourcesCacheReusesVersionUnderConcurrency(t *testing.T) {
	var cache dictionarySourcesCache
	bodies := make([][]byte, 16)
	var requests sync.WaitGroup
	requests.Add(len(bodies))
	for i := range bodies {
		go func() {
			defer requests.Done()
			version, body, err := cache.response(taxonomy.Definition{Version: 1})
			if err != nil || version != 1 {
				t.Error(version, err)
			}
			bodies[i] = body
		}()
	}
	requests.Wait()
	body := bodies[0]
	for _, again := range bodies {
		if len(again) == 0 || len(body) == 0 || &again[0] != &body[0] {
			t.Fatal("кэш версии построен повторно")
		}
	}

	version, next, err := cache.response(taxonomy.Definition{Version: 2})
	if err != nil || version != 2 || &next[0] == &body[0] {
		t.Fatal("новая версия не пересчитана", version, err)
	}
	version, late, err := cache.response(taxonomy.Definition{Version: 1})
	if err != nil || version != 2 || &late[0] != &next[0] {
		t.Fatal("поздний старый снимок затёр кэш", version, err)
	}
}
