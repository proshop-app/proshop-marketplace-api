package referenceapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/referenceapi"
)

func TestClientUsesSeparateBackendRightsAndPreservesRevision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/reference/b1/entries/e1" || r.URL.RawQuery != "x=1" ||
			r.Header.Get("Authorization") != "Bearer writer" || r.Header.Get("X-Reference-Actor") != "operator-1" ||
			r.Header.Get("If-Match") != `"12"` || r.Method != http.MethodPatch {
			t.Errorf("upstream запрос: %s %s %v", r.Method, r.URL, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"canonical":"Значение"}` {
			t.Errorf("тело: %s", body)
		}
		w.Header().Set("ETag", `"13"`)
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"code":"reference.entry_changed","message":"перечитать"}`)
	}))
	defer server.Close()
	client := referenceapi.New(referenceapi.Config{BaseURL: server.URL, ReadToken: "reader", WriteToken: "writer"})
	response, err := client.Do(context.Background(), ports.ReferenceRequest{
		Method:     http.MethodPatch,
		Dictionary: "b1",
		Resource:   "entries/e1",
		Query:      "x=1",
		Body:       json.RawMessage(`{"canonical":"Значение"}`),
		Actor:      "operator-1",
		IfMatch:    `"12"`,
		Write:      true,
	})
	if err != nil || response.Status != 409 || response.ETag != `"13"` ||
		!strings.Contains(string(response.Body), "REFERENCE_ENTRY_CHANGED") {
		t.Fatalf("совместимость отказа: %+v %v", response, err)
	}
}

func TestClientReadsCanonicalBrandAndClosedValuesFromOwner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer reader" || r.Header.Get("X-Reference-Actor") != "" {
			t.Errorf("права читателя: %v", r.Header)
		}
		if strings.HasSuffix(r.URL.Path, "/resolve") {
			_, _ = io.WriteString(w, `{"schemaVersion":1,"dictionary":"brand","match":{"entryId":"stable-id"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"schemaVersion":1,"dictionary":"a1","kind":"closed","revision":13,`+
			`"items":[{"id":"e1","value":"v1","order":0,"meta":{"hex":"#ffffff"}}]}`)
	}))
	defer server.Close()
	client := referenceapi.New(referenceapi.Config{BaseURL: server.URL, ReadToken: "reader", WriteToken: "writer"})
	id, found, err := client.Resolve(context.Background(), "Вариант")
	if err != nil || !found || id != "stable-id" {
		t.Fatalf("разрешение бренда: %s %v %v", id, found, err)
	}
	values, err := client.Values(context.Background(), "a1")
	if err != nil || len(values) != 1 || values[0].ID != "e1" || values[0].Meta.Hex != "#ffffff" {
		t.Fatalf("общие значения: %+v %v", values, err)
	}
}

func TestClientReadsDictionaryRevisionsWithReaderRights(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/reference" || r.Method != http.MethodGet ||
			r.Header.Get("Authorization") != "Bearer reader" {
			t.Errorf("сверка ревизий: %s %s %v", r.Method, r.URL, r.Header)
		}
		_, _ = io.WriteString(w, `{"schemaVersion":1,"dictionaries":[`+
			`{"code":"a1","kind":"closed","revision":12},{"code":"brand","kind":"open","revision":40}]}`)
	}))
	defer server.Close()
	client := referenceapi.New(referenceapi.Config{BaseURL: server.URL, ReadToken: "reader", WriteToken: "writer"})
	revisions, err := client.Revisions(context.Background())
	want := []ports.ReferenceRevision{
		{Code: "a1", Kind: "closed", Revision: 12},
		{Code: "brand", Kind: "open", Revision: 40},
	}
	if err != nil || !reflect.DeepEqual(revisions, want) {
		t.Fatalf("ревизии словарей: %+v %v", revisions, err)
	}
}

func TestClientRejectsMalformedRevisions(t *testing.T) {
	bodies := []string{
		`{"schemaVersion":2,"dictionaries":[]}`,
		`{"schemaVersion":1}`,
		`{"schemaVersion":1,"dictionaries":[{"code":"a1","revision":1},{"code":"a1","revision":2}]}`,
		`{"schemaVersion":1,"dictionaries":[{"code":"A-1","revision":1}]}`,
		`{"schemaVersion":1,"dictionaries":[{"code":"a1","revision":-1}]}`,
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			client := referenceapi.New(referenceapi.Config{BaseURL: server.URL, ReadToken: "reader"})
			if _, err := client.Revisions(context.Background()); err == nil {
				t.Fatal("неверный список словарей принят")
			}
		})
	}
}

func TestClientRejectsRedirectMalformedAndUnavailableOwner(t *testing.T) {
	for _, body := range []string{`not JSON`, `{"schemaVersion":2}`, `{"schemaVersion":1,"dictionary":"other"}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			client := referenceapi.New(referenceapi.Config{BaseURL: server.URL, ReadToken: "reader"})
			if _, _, err := client.Resolve(context.Background(), "x"); err == nil {
				t.Fatal("неверный ответ превратился в промах")
			}
			if _, err := client.Values(context.Background(), "a1"); err == nil {
				t.Fatal("неверный ответ превратился в пустой словарь")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.invalid", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := referenceapi.New(referenceapi.Config{BaseURL: server.URL, ReadToken: "reader"})
	if _, err := client.Values(context.Background(), "a1"); err == nil {
		t.Fatal("редирект с токеном разрешён")
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, _, err := client.Resolve(ctx, "x"); err == nil {
		t.Fatal("просроченный resolver не вернул отказ")
	}
}
