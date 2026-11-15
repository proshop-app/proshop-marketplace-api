package searchapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestSearchExactWireContract(t *testing.T) {
	legacy, err := os.ReadFile("testdata/search_query_legacy.json")
	if err != nil {
		t.Fatal(err)
	}
	legacy = bytes.TrimSpace(legacy)
	response, err := os.ReadFile("testdata/search_no_buckets.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		query ports.OfferQuery
	}{
		{
			name:  "без флага",
			query: ports.OfferQuery{Text: "товра"},
		},
		{
			name:  "false",
			query: ports.OfferQuery{Text: "товра", Exact: false},
		},
		{
			name:  "true",
			query: ports.OfferQuery{Text: "товра", Exact: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New(Config{
				URL:     "http://search.test",
				Token:   strings.Repeat("m", 32),
				Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			client.client.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost || r.URL.Path != apiPath+"/search" {
					t.Fatal("неожиданный маршрут запроса", r.Method, r.URL.Path)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				want := string(legacy)
				if tc.query.Exact {
					want = `{"exact":true,` + want[1:]
				}
				if string(body) != want {
					t.Fatalf("нарушен JSON-контракт:\n%s\nожидалось:\n%s", body, want)
				}
				w := httptest.NewRecorder()
				_, _ = w.Write(response)
				return w.Result(), nil
			})
			if _, err := client.Search(context.Background(), tc.query); err != nil {
				t.Fatal(err)
			}
		})
	}
}
