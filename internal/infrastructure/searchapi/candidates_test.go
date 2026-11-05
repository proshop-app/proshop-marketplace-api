package searchapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestCandidatesContract(t *testing.T) {
	fixture, err := os.ReadFile("testdata/attribute-values.json")
	if err != nil {
		t.Fatal(err)
	}
	token := "candidates-token-0123456789abcdef012345"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/internal/catalog/attribute-values" ||
			r.Header.Get("X-Admin-Id") != token || r.URL.Query().Get("key") != "b1" ||
			r.URL.Query().Get("size") != "500" || r.URL.Query().Get("after") != "next/+" {
			t.Error("нарушен контракт запроса")
		}
		_, _ = w.Write(fixture)
	}))
	defer server.Close()
	client, err := NewCandidates(Config{URL: server.URL, Token: token, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.AttributeValues(context.Background(), "b1", 500, "next/+")
	if err != nil || page.After != "next/+" || len(page.Items) != 1 || page.Items[0].Documents != 12 {
		t.Fatalf("страница: %+v, %v", page, err)
	}
}

func TestCandidatesErrors(t *testing.T) {
	for _, status := range []int{400, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(status)
			}))
			defer server.Close()
			client, err := NewCandidates(Config{
				URL:     server.URL,
				Token:   "candidates-token-0123456789abcdef012345",
				Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.AttributeValues(context.Background(), "b1", 500, ""); err == nil {
				t.Fatal("ошибка поиска потеряна")
			}
		})
	}
}
