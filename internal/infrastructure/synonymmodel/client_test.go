package synonymmodel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestModelProviderCarriesBoundedTaxonomyInput(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil || r.Header.Get("Authorization") != "Bearer fixture" ||
			r.Header.Get("Idempotency-Key") != "input-id" || req.Model != "fixture" ||
			req.PromptVersion != "1" || req.Prompt == "" || len(req.Concepts) != 1 || req.Concepts[0].ID != "section" {
			t.Errorf("запрос модели: %+v, %v", req, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body := `{"proposals":[{"categoryId":"section","from":"Первое","to":"Второе","relation":"equivalent"}]}`
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	client, err := New(srv.URL, "fixture", "fixture", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = srv.Client().Transport
	concepts := []ports.SynonymModelConcept{{ID: "section", Name: "Второе"}}
	proposals, err := client.Propose(context.Background(), "input-id", concepts)
	if err != nil || len(proposals) != 1 || proposals[0].Relation != "equivalent" {
		t.Fatalf("ответ модели: %+v, %v", proposals, err)
	}
}

func TestModelProviderRejectsRedirectAndOversizedResponse(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
			return
		}
		_, _ = w.Write([]byte(strings.Repeat(" ", responseLimit+1)))
	}))
	defer server.Close()

	for _, path := range []string{"/redirect", "/large"} {
		client, err := New(server.URL+path, "fixture", "fixture", time.Second)
		if err != nil {
			t.Fatal(err)
		}
		client.http.Transport = server.Client().Transport
		if _, err := client.Propose(context.Background(), "input-id", nil); err == nil {
			t.Fatalf("неверный ответ принят: %s", path)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("выполнено перенаправление: %d вызова", calls.Load())
	}
}
